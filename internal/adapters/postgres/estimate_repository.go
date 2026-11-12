package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type estimateDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type EstimateRepository struct {
	db           estimateDB
	queryTimeout time.Duration
}

func NewEstimateRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *EstimateRepository {
	return &EstimateRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *EstimateRepository) ListEstimates(ctx context.Context, filter domain.EstimateFilter) ([]domain.Estimate, int, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	status := estimateDatabaseStatus(filter.Status)
	const predicate = `s."companyId"::text = any($1::text[])
  and ($2 = '' or e.status::text = $2)
  and ($3 = '' or s."technicianId"::text = $3)
  and ($4 <> 'TECHNICIAN' or s."technicianId"::text = $5)`
	args := []any{filter.Actor.CompanyIDs, status, filter.TechnicianID, filter.Actor.Role, filter.Actor.UserID}
	var total int
	if err := r.db.QueryRow(queryCtx, `
select count(*)
from public.estimate as e
join public."service" as s on s.id = e."serviceId"
where `+predicate, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count estimates: %w", err)
	}
	rows, err := r.db.Query(queryCtx, `
select e.id::text, e."serviceId"::text, e.amount::text, e.description, e.notes,
       e.status::text, e."completedAt", e."createdAt", e."updatedAt"
from public.estimate as e
join public."service" as s on s.id = e."serviceId"
where `+predicate+`
order by e."createdAt" desc, e.id desc
limit $6 offset $7
`, append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)...)
	if err != nil {
		return nil, 0, fmt.Errorf("query estimates: %w", err)
	}
	defer rows.Close()
	estimates := make([]domain.Estimate, 0)
	for rows.Next() {
		estimate, err := scanEstimate(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan estimate: %w", err)
		}
		estimates = append(estimates, estimate)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read estimates: %w", err)
	}
	return estimates, total, nil
}

func (r *EstimateRepository) GetEstimate(ctx context.Context, id string, actor domain.ServiceActor) (domain.Estimate, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	estimate, err := scanEstimate(r.db.QueryRow(queryCtx, `
select e.id::text, e."serviceId"::text, e.amount::text, e.description, e.notes,
       e.status::text, e."completedAt", e."createdAt", e."updatedAt"
from public.estimate as e
join public."service" as s on s.id = e."serviceId"
where e.id = $1::uuid
  and s."companyId"::text = any($2::text[])
  and ($3 <> 'TECHNICIAN' or s."technicianId"::text = $4)
`, id, actor.CompanyIDs, actor.Role, actor.UserID))
	if err != nil {
		return domain.Estimate{}, mapEstimateRepositoryError(err)
	}
	return estimate, nil
}

func (r *EstimateRepository) CreateEstimate(ctx context.Context, draft domain.EstimateDraft, actor domain.ServiceActor) (domain.Estimate, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Estimate{}, fmt.Errorf("begin estimate creation: %w", err)
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
		return domain.Estimate{}, mapEstimateRepositoryError(err)
	}
	if (status != "IN_PROGRESS" && status != "COMPLETED") || technicianID != actor.UserID {
		return domain.Estimate{}, outbound.ErrConflict
	}
	estimate, err := scanEstimate(tx.QueryRow(queryCtx, `
insert into public.estimate ("serviceId", amount, description, notes, status)
values ($1::uuid, $2::numeric, $3, $4, 'PENDING')
returning id::text, "serviceId"::text, amount::text, description, notes,
          status::text, "completedAt", "createdAt", "updatedAt"
`, draft.ServiceID, draft.Amount, draft.Description, draft.Notes))
	if err != nil {
		return domain.Estimate{}, mapEstimateRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]string{"estimateId": estimate.ID, "amount": estimate.Amount})
	if err != nil {
		return domain.Estimate{}, fmt.Errorf("encode estimate creation audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: estimate.ServiceID, UserID: actor.UserID, Action: "ESTIMATE_CREATED",
		Description: "Presupuesto enviado", Metadata: metadata,
	}); err != nil {
		return domain.Estimate{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Estimate{}, fmt.Errorf("commit estimate creation: %w", err)
	}
	return estimate, nil
}

func (r *EstimateRepository) FinalizeEstimate(ctx context.Context, id string, actor domain.ServiceActor) (domain.Estimate, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Estimate{}, fmt.Errorf("begin estimate finalization: %w", err)
	}
	defer rollback(tx)

	current, err := scanEstimate(tx.QueryRow(queryCtx, `
select e.id::text, e."serviceId"::text, e.amount::text, e.description, e.notes,
       e.status::text, e."completedAt", e."createdAt", e."updatedAt"
from public.estimate as e
join public."service" as s on s.id = e."serviceId"
where e.id = $1::uuid and s."companyId"::text = any($2::text[])
for update
`, id, actor.CompanyIDs))
	if err != nil {
		return domain.Estimate{}, mapEstimateRepositoryError(err)
	}
	if current.Status != "PENDING" {
		return domain.Estimate{}, outbound.ErrConflict
	}
	estimate, err := scanEstimate(tx.QueryRow(queryCtx, `
update public.estimate
set status = 'COMPLETED', "completedAt" = now(), "updatedAt" = now()
where id = $1::uuid and status::text = 'PENDING'
returning id::text, "serviceId"::text, amount::text, description, notes,
          status::text, "completedAt", "createdAt", "updatedAt"
`, id))
	if err != nil {
		return domain.Estimate{}, mapEstimateRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]string{"estimateId": estimate.ID, "from": "PENDING", "to": "FINALIZED"})
	if err != nil {
		return domain.Estimate{}, fmt.Errorf("encode estimate finalization audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: estimate.ServiceID, UserID: actor.UserID, Action: "ESTIMATE_FINALIZED",
		Description: "Presupuesto finalizado", Metadata: metadata,
	}); err != nil {
		return domain.Estimate{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Estimate{}, fmt.Errorf("commit estimate finalization: %w", err)
	}
	return estimate, nil
}

func estimateDatabaseStatus(status string) string {
	if status == "FINALIZED" {
		return "COMPLETED"
	}
	return status
}

func scanEstimate(row pgx.Row) (domain.Estimate, error) {
	var estimate domain.Estimate
	var description, notes pgtype.Text
	var completedAt pgtype.Timestamptz
	if err := row.Scan(
		&estimate.ID, &estimate.ServiceID, &estimate.Amount, &description, &notes,
		&estimate.Status, &completedAt, &estimate.CreatedAt, &estimate.UpdatedAt,
	); err != nil {
		return domain.Estimate{}, err
	}
	estimate.Description = textPointer(description)
	estimate.Notes = textPointer(notes)
	if completedAt.Valid {
		value := completedAt.Time
		estimate.CompletedAt = &value
	}
	if estimate.Status == "COMPLETED" {
		estimate.Status = "FINALIZED"
	}
	return estimate, nil
}

func mapEstimateRepositoryError(err error) error {
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
	return fmt.Errorf("estimate repository operation: %w", err)
}

func (r *EstimateRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.EstimateRepository = (*EstimateRepository)(nil)
