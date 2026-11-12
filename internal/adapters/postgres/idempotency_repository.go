package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/ports/outbound"
)

type IdempotencyRepository struct {
	pool         *pgxpool.Pool
	queryTimeout time.Duration
}

func NewIdempotencyRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *IdempotencyRepository {
	return &IdempotencyRepository{pool: pool, queryTimeout: queryTimeout}
}

func (r *IdempotencyRepository) Claim(ctx context.Context, key outbound.IdempotencyKey) (outbound.IdempotencyRecord, bool, error) {
	queryCtx, cancel := idempotencyQueryContext(ctx, r.queryTimeout)
	defer cancel()

	result, err := r.pool.Exec(queryCtx, `
insert into public.api_idempotency_keys (user_id, operation, idempotency_key, request_hash)
values ($1::uuid, $2, $3, $4)
on conflict (user_id, operation, idempotency_key) do nothing
`, key.UserID, key.Operation, key.Key, key.RequestHash)
	if err != nil {
		return outbound.IdempotencyRecord{}, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	if result.RowsAffected() == 1 {
		return outbound.IdempotencyRecord{RequestHash: key.RequestHash}, true, nil
	}

	var record outbound.IdempotencyRecord
	var status pgtype.Int2
	var contentType pgtype.Text
	var response []byte
	var completedAt pgtype.Timestamptz
	err = r.pool.QueryRow(queryCtx, `
select request_hash, response_status, response_content_type, response_body, completed_at
from public.api_idempotency_keys
where user_id = $1::uuid and operation = $2 and idempotency_key = $3
`, key.UserID, key.Operation, key.Key).Scan(
		&record.RequestHash, &status, &contentType, &response, &completedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return outbound.IdempotencyRecord{}, false, fmt.Errorf("claimed idempotency record disappeared: %w", err)
		}
		return outbound.IdempotencyRecord{}, false, fmt.Errorf("load idempotency record: %w", err)
	}
	record.Completed = completedAt.Valid
	if status.Valid {
		record.StatusCode = int(status.Int16)
	}
	if contentType.Valid {
		record.ContentType = contentType.String
	}
	record.Response = response
	return record, false, nil
}

func (r *IdempotencyRepository) Complete(ctx context.Context, key outbound.IdempotencyKey, status int, contentType string, response []byte) error {
	queryCtx, cancel := idempotencyQueryContext(ctx, r.queryTimeout)
	defer cancel()

	result, err := r.pool.Exec(queryCtx, `
update public.api_idempotency_keys
set response_status = $5, response_content_type = $6, response_body = $7, completed_at = now()
where user_id = $1::uuid and operation = $2 and idempotency_key = $3
  and request_hash = $4 and completed_at is null
`, key.UserID, key.Operation, key.Key, key.RequestHash, status, contentType, response)
	if err != nil {
		return fmt.Errorf("complete idempotency record: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("idempotency record was not pending")
	}
	return nil
}

func (r *IdempotencyRepository) Release(ctx context.Context, key outbound.IdempotencyKey) error {
	queryCtx, cancel := idempotencyQueryContext(ctx, r.queryTimeout)
	defer cancel()

	_, err := r.pool.Exec(queryCtx, `
delete from public.api_idempotency_keys
where user_id = $1::uuid and operation = $2 and idempotency_key = $3
  and request_hash = $4 and completed_at is null
`, key.UserID, key.Operation, key.Key, key.RequestHash)
	if err != nil {
		return fmt.Errorf("release idempotency record: %w", err)
	}
	return nil
}

var _ outbound.IdempotencyRepository = (*IdempotencyRepository)(nil)

func idempotencyQueryContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
