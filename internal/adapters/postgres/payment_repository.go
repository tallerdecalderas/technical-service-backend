package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type paymentDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type PaymentRepository struct {
	db           paymentDB
	queryTimeout time.Duration
}

func NewPaymentRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *PaymentRepository {
	return &PaymentRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *PaymentRepository) ListPayments(ctx context.Context, filter domain.PaymentFilter) ([]domain.Payment, int, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	const predicate = `s."companyId"::text = any($1::text[])
  and ($2 = '' or p."technicianId"::text = $2)`
	var total int
	if err := r.db.QueryRow(queryCtx, `
select count(*)
from public.payment as p
join public."service" as s on s.id = p."serviceId"
where `+predicate, filter.Actor.CompanyIDs, filter.TechnicianID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count payments: %w", err)
	}
	rows, err := r.db.Query(queryCtx, `
select p.id::text, p."serviceId"::text, p."technicianId"::text,
       p."settlementId"::text, p.method::text, p."amountPaid"::text,
       p."debtAmount"::text, p."createdAt", p."updatedAt"
from public.payment as p
join public."service" as s on s.id = p."serviceId"
where `+predicate+`
order by p."createdAt" desc, p.id desc
limit $3 offset $4
`, filter.Actor.CompanyIDs, filter.TechnicianID, filter.PageSize, (filter.Page-1)*filter.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("query payments: %w", err)
	}
	defer rows.Close()
	payments := make([]domain.Payment, 0)
	for rows.Next() {
		payment, err := scanPayment(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, payment)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read payments: %w", err)
	}
	return payments, total, nil
}

func (r *PaymentRepository) GetPayment(ctx context.Context, id string, actor domain.ServiceActor) (domain.Payment, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	payment, err := scanPayment(r.db.QueryRow(queryCtx, `
select p.id::text, p."serviceId"::text, p."technicianId"::text,
       p."settlementId"::text, p.method::text, p."amountPaid"::text,
       p."debtAmount"::text, p."createdAt", p."updatedAt"
from public.payment as p
join public."service" as s on s.id = p."serviceId"
where p.id = $1::uuid
  and s."companyId"::text = any($2::text[])
  and ($3 <> 'TECHNICIAN' or p."technicianId"::text = $4)
`, id, actor.CompanyIDs, actor.Role, actor.UserID))
	if err != nil {
		return domain.Payment{}, mapPaymentRepositoryError(err)
	}
	return payment, nil
}

func (r *PaymentRepository) CreatePayment(ctx context.Context, draft domain.PaymentDraft, actor domain.ServiceActor) (domain.Payment, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("begin payment creation: %w", err)
	}
	defer rollback(tx)

	var status, technicianID string
	if err := tx.QueryRow(queryCtx, `
select s.status::text, s."technicianId"::text
from public."service" as s
where s.id = $1::uuid
  and s."companyId"::text = any($2::text[])
  and s."technicianId"::text = $3
for update
`, draft.ServiceID, actor.CompanyIDs, actor.UserID).Scan(&status, &technicianID); err != nil {
		return domain.Payment{}, mapPaymentRepositoryError(err)
	}
	if status != "IN_PROGRESS" || technicianID != actor.UserID || draft.TechnicianID != actor.UserID {
		return domain.Payment{}, outbound.ErrConflict
	}
	payment, err := scanPayment(tx.QueryRow(queryCtx, `
insert into public.payment ("serviceId", "technicianId", method, "amountPaid", "debtAmount")
values ($1::uuid, $2::uuid, $3, $4::numeric, $5::numeric)
returning id::text, "serviceId"::text, "technicianId"::text, "settlementId"::text,
          method::text, "amountPaid"::text, "debtAmount"::text, "createdAt", "updatedAt"
`, draft.ServiceID, draft.TechnicianID, draft.Method, draft.AmountPaid, draft.DebtAmount))
	if err != nil {
		return domain.Payment{}, mapPaymentRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]string{
		"paymentId": payment.ID, "method": payment.Method, "amountPaid": payment.AmountPaid, "debtAmount": payment.DebtAmount,
	})
	if err != nil {
		return domain.Payment{}, fmt.Errorf("encode payment creation audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: draft.ServiceID, UserID: actor.UserID, Action: "PAYMENT_CREATED",
		Description: "Cobro registrado", Metadata: metadata,
	}); err != nil {
		return domain.Payment{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Payment{}, fmt.Errorf("commit payment creation: %w", err)
	}
	return payment, nil
}

func (r *PaymentRepository) UpdatePayment(ctx context.Context, id string, patch domain.PaymentPatch, actor domain.ServiceActor) (domain.Payment, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("begin payment update: %w", err)
	}
	defer rollback(tx)

	current, err := scanPayment(tx.QueryRow(queryCtx, `
select p.id::text, p."serviceId"::text, p."technicianId"::text,
       p."settlementId"::text, p.method::text, p."amountPaid"::text,
       p."debtAmount"::text, p."createdAt", p."updatedAt"
from public.payment as p
join public."service" as s on s.id = p."serviceId"
where p.id = $1::uuid and s."companyId"::text = any($2::text[])
for update
`, id, actor.CompanyIDs))
	if err != nil {
		return domain.Payment{}, mapPaymentRepositoryError(err)
	}
	query, args, err := buildPaymentUpdateQuery(id, patch)
	if err != nil {
		return domain.Payment{}, err
	}
	updated, err := scanPayment(tx.QueryRow(queryCtx, query, args...))
	if err != nil {
		return domain.Payment{}, mapPaymentRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"paymentId": current.ID,
		"previous":  map[string]string{"method": current.Method, "amountPaid": current.AmountPaid, "debtAmount": current.DebtAmount},
		"next":      map[string]string{"method": updated.Method, "amountPaid": updated.AmountPaid, "debtAmount": updated.DebtAmount},
	})
	if err != nil {
		return domain.Payment{}, fmt.Errorf("encode payment update audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: updated.ServiceID, UserID: actor.UserID, Action: "PAYMENT_UPDATED",
		Description: "Cobro actualizado", Metadata: metadata,
	}); err != nil {
		return domain.Payment{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Payment{}, fmt.Errorf("commit payment update: %w", err)
	}
	return updated, nil
}

func buildPaymentUpdateQuery(id string, patch domain.PaymentPatch) (string, []any, error) {
	fields := []struct {
		name  string
		value *string
	}{
		{name: "method", value: patch.Method.Value},
		{name: "amountPaid", value: patch.AmountPaid.Value},
		{name: "debtAmount", value: patch.DebtAmount.Value},
	}
	sets := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+1)
	for _, field := range fields {
		isSet := (field.name == "method" && patch.Method.Set) ||
			(field.name == "amountPaid" && patch.AmountPaid.Set) ||
			(field.name == "debtAmount" && patch.DebtAmount.Set)
		if !isSet {
			continue
		}
		args = append(args, field.value)
		sets = append(sets, fmt.Sprintf("%q = $%d", field.name, len(args)))
	}
	if len(sets) == 0 {
		return "", nil, outbound.ErrConflict
	}
	sets = append(sets, `"updatedAt" = now()`)
	args = append(args, id)
	return fmt.Sprintf(`
update public.payment
set %s
where id = $%d::uuid
returning id::text, "serviceId"::text, "technicianId"::text, "settlementId"::text,
          method::text, "amountPaid"::text, "debtAmount"::text, "createdAt", "updatedAt"
`, strings.Join(sets, ", "), len(args)), args, nil
}

func scanPayment(row pgx.Row) (domain.Payment, error) {
	var payment domain.Payment
	var settlementID pgtype.Text
	if err := row.Scan(
		&payment.ID, &payment.ServiceID, &payment.TechnicianID, &settlementID,
		&payment.Method, &payment.AmountPaid, &payment.DebtAmount,
		&payment.CreatedAt, &payment.UpdatedAt,
	); err != nil {
		return domain.Payment{}, err
	}
	payment.SettlementID = textPointer(settlementID)
	return payment, nil
}

func mapPaymentRepositoryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return outbound.ErrRecordNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505", "23514":
			return outbound.ErrConflict
		case "23503", "22P02":
			return outbound.ErrRecordNotFound
		}
	}
	return fmt.Errorf("payment repository operation: %w", err)
}

func (r *PaymentRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.PaymentRepository = (*PaymentRepository)(nil)
