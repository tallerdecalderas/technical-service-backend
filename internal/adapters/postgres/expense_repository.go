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
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type expenseDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type ExpenseRepository struct {
	db           expenseDB
	queryTimeout time.Duration
}

func NewExpenseRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *ExpenseRepository {
	return &ExpenseRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *ExpenseRepository) ListExpenses(ctx context.Context, filter domain.ExpenseFilter) ([]domain.Expense, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.db.Query(queryCtx, `
select e.id::text, e."serviceId"::text, e.concept::text, e.amount::text,
       e.currency::text, e."recordedById"::text, e."createdAt", e."updatedAt"
from public."serviceExpense" as e
join public."service" as s on s.id = e."serviceId"
where s."companyId"::text = any($1::text[])
  and e."serviceId" = $2::uuid
  and ($3 <> 'TECHNICIAN' or s."technicianId"::text = $4)
order by e."createdAt" asc, e.id asc
`, filter.Actor.CompanyIDs, filter.ServiceID, filter.Actor.Role, filter.Actor.UserID)
	if err != nil {
		return nil, fmt.Errorf("query expenses: %w", err)
	}
	defer rows.Close()
	expenses := make([]domain.Expense, 0)
	for rows.Next() {
		expense, err := scanExpense(rows)
		if err != nil {
			return nil, fmt.Errorf("scan expense: %w", err)
		}
		expenses = append(expenses, expense)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read expenses: %w", err)
	}
	return expenses, nil
}

func (r *ExpenseRepository) GetExpense(ctx context.Context, id string, actor domain.ServiceActor) (domain.Expense, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	expense, err := scanExpense(r.db.QueryRow(queryCtx, `
select e.id::text, e."serviceId"::text, e.concept::text, e.amount::text,
       e.currency::text, e."recordedById"::text, e."createdAt", e."updatedAt"
from public."serviceExpense" as e
join public."service" as s on s.id = e."serviceId"
where e.id = $1::uuid
  and s."companyId"::text = any($2::text[])
  and ($3 <> 'TECHNICIAN' or s."technicianId"::text = $4)
`, id, actor.CompanyIDs, actor.Role, actor.UserID))
	if err != nil {
		return domain.Expense{}, mapExpenseRepositoryError(err)
	}
	return expense, nil
}

func (r *ExpenseRepository) CreateExpense(ctx context.Context, draft domain.ExpenseDraft, actor domain.ServiceActor) (domain.Expense, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Expense{}, fmt.Errorf("begin expense creation: %w", err)
	}
	defer rollback(tx)

	var status, technicianID string
	if err := tx.QueryRow(queryCtx, `
select s.status::text, s."technicianId"::text
from public."service" as s
where s.id = $1::uuid
  and s."companyId"::text = any($2::text[])
for update
`, draft.ServiceID, actor.CompanyIDs).Scan(&status, &technicianID); err != nil {
		return domain.Expense{}, mapExpenseRepositoryError(err)
	}

	if actor.Role == "TECHNICIAN" {
		if status != "IN_PROGRESS" && status != "COMPLETED" {
			return domain.Expense{}, outbound.ErrConflict
		}
		if technicianID != actor.UserID || draft.RecordedByID != actor.UserID {
			return domain.Expense{}, outbound.ErrConflict
		}
	}

	if err := ensureServiceNotLiquidated(queryCtx, tx, draft.ServiceID); err != nil {
		return domain.Expense{}, err
	}

	expense, err := scanExpense(tx.QueryRow(queryCtx, `
insert into public."serviceExpense" ("serviceId", concept, amount, currency, "recordedById", "createdAt", "updatedAt")
values ($1::uuid, $2, $3::numeric, $4, $5::uuid, now(), now())
returning id::text, "serviceId"::text, concept::text, amount::text,
          currency::text, "recordedById"::text, "createdAt", "updatedAt"
`, draft.ServiceID, draft.Concept, draft.Amount, draft.Currency, draft.RecordedByID))
	if err != nil {
		return domain.Expense{}, mapExpenseRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]string{
		"expenseId": expense.ID, "concept": expense.Concept, "amount": expense.Amount, "currency": expense.Currency,
	})
	if err != nil {
		return domain.Expense{}, fmt.Errorf("encode expense creation audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: draft.ServiceID, UserID: actor.UserID, Action: "EXPENSE_CREATED",
		Description: "Gasto registrado", Metadata: metadata,
	}); err != nil {
		return domain.Expense{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Expense{}, fmt.Errorf("commit expense creation: %w", err)
	}
	return expense, nil
}

func (r *ExpenseRepository) UpdateExpense(ctx context.Context, serviceID, expenseID string, patch domain.ExpensePatch, actor domain.ServiceActor) (domain.Expense, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Expense{}, fmt.Errorf("begin expense update: %w", err)
	}
	defer rollback(tx)

	current, err := scanExpense(tx.QueryRow(queryCtx, `
select e.id::text, e."serviceId"::text, e.concept::text, e.amount::text,
       e.currency::text, e."recordedById"::text, e."createdAt", e."updatedAt"
from public."serviceExpense" as e
join public."service" as s on s.id = e."serviceId"
where e.id = $1::uuid and e."serviceId" = $2::uuid and s."companyId"::text = any($3::text[])
for update
`, expenseID, serviceID, actor.CompanyIDs))
	if err != nil {
		return domain.Expense{}, mapExpenseRepositoryError(err)
	}

	if err := ensureServiceNotLiquidated(queryCtx, tx, current.ServiceID); err != nil {
		return domain.Expense{}, err
	}

	query, args, err := buildExpenseUpdateQuery(expenseID, patch)
	if err != nil {
		return domain.Expense{}, err
	}
	updated, err := scanExpense(tx.QueryRow(queryCtx, query, args...))
	if err != nil {
		return domain.Expense{}, mapExpenseRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]any{
		"expenseId": current.ID,
		"previous":  map[string]string{"concept": current.Concept, "amount": current.Amount, "currency": current.Currency},
		"next":      map[string]string{"concept": updated.Concept, "amount": updated.Amount, "currency": updated.Currency},
	})
	if err != nil {
		return domain.Expense{}, fmt.Errorf("encode expense update audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: updated.ServiceID, UserID: actor.UserID, Action: "EXPENSE_UPDATED",
		Description: "Gasto actualizado", Metadata: metadata,
	}); err != nil {
		return domain.Expense{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Expense{}, fmt.Errorf("commit expense update: %w", err)
	}
	return updated, nil
}

func (r *ExpenseRepository) DeleteExpense(ctx context.Context, serviceID, expenseID string, actor domain.ServiceActor) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("begin expense deletion: %w", err)
	}
	defer rollback(tx)

	current, err := scanExpense(tx.QueryRow(queryCtx, `
select e.id::text, e."serviceId"::text, e.concept::text, e.amount::text,
       e.currency::text, e."recordedById"::text, e."createdAt", e."updatedAt"
from public."serviceExpense" as e
join public."service" as s on s.id = e."serviceId"
where e.id = $1::uuid and e."serviceId" = $2::uuid and s."companyId"::text = any($3::text[])
for update
`, expenseID, serviceID, actor.CompanyIDs))
	if err != nil {
		return mapExpenseRepositoryError(err)
	}

	if err := ensureServiceNotLiquidated(queryCtx, tx, current.ServiceID); err != nil {
		return err
	}

	if _, err := tx.Exec(queryCtx, `
delete from public."serviceExpense"
where id = $1::uuid and "serviceId" = $2::uuid
`, expenseID, serviceID); err != nil {
		return mapExpenseRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]string{
		"expenseId": current.ID, "concept": current.Concept, "amount": current.Amount, "currency": current.Currency,
	})
	if err != nil {
		return fmt.Errorf("encode expense deletion audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: current.ServiceID, UserID: actor.UserID, Action: "EXPENSE_REMOVED",
		Description: "Gasto retirado", Metadata: metadata,
	}); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("commit expense deletion: %w", err)
	}
	return nil
}

func buildExpenseUpdateQuery(id string, patch domain.ExpensePatch) (string, []any, error) {
	fields := []struct {
		name  string
		value *string
		set   bool
	}{
		{name: "concept", value: patch.Concept.Value, set: patch.Concept.Set},
		{name: "amount", value: patch.Amount.Value, set: patch.Amount.Set},
		{name: "currency", value: patch.Currency.Value, set: patch.Currency.Set},
	}
	sets := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+1)
	for _, field := range fields {
		if !field.set || field.value == nil {
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
update public."serviceExpense"
set %s
where id = $%d::uuid
returning id::text, "serviceId"::text, concept::text, amount::text,
          currency::text, "recordedById"::text, "createdAt", "updatedAt"
`, strings.Join(sets, ", "), len(args)), args, nil
}

func ensureServiceNotLiquidated(ctx context.Context, tx pgx.Tx, serviceID string) error {
	var liquidated bool
	if err := tx.QueryRow(ctx, `
select exists (
    select 1
    from public.payment as p
    join public.settlement as s on s.id = p."settlementId"
    where p."serviceId" = $1::uuid and s.status::text in ('PAID', 'LIQUIDATED')
)
`, serviceID).Scan(&liquidated); err != nil {
		return fmt.Errorf("check service settlement state: %w", err)
	}
	if liquidated {
		return outbound.ErrConflict
	}
	return nil
}

func scanExpense(row pgx.Row) (domain.Expense, error) {
	var expense domain.Expense
	if err := row.Scan(
		&expense.ID, &expense.ServiceID, &expense.Concept, &expense.Amount,
		&expense.Currency, &expense.RecordedByID, &expense.CreatedAt, &expense.UpdatedAt,
	); err != nil {
		return domain.Expense{}, err
	}
	return expense, nil
}

func mapExpenseRepositoryError(err error) error {
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
	return fmt.Errorf("expense repository operation: %w", err)
}

func (r *ExpenseRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.ExpenseRepository = (*ExpenseRepository)(nil)
