package postgres

import (
	"context"
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

var ErrInvalidClientPatch = errors.New("invalid client patch")

type clientDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type ClientRepository struct {
	db           clientDB
	queryTimeout time.Duration
}

func NewClientRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *ClientRepository {
	return &ClientRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *ClientRepository) ListClients(ctx context.Context, filter outbound.ClientFilter) ([]domain.Client, int, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	const predicate = `($1 = '' or coalesce(c.name, '') ilike '%' || $1 || '%' or coalesce(c.email, '') ilike '%' || $1 || '%' or c.phone ilike '%' || $1 || '%')
and ($2 <> 'TECHNICIAN' or exists (
    select 1
    from public."service" as assigned_service
    where assigned_service."clientId" = c.id
      and assigned_service."companyId"::text = any($3::text[])
      and assigned_service."technicianId"::text = $4
))`
	var total int
	if err := r.db.QueryRow(queryCtx, `select count(*) from public."client" as c where `+predicate,
		filter.Search, filter.Role, filter.CompanyIDs, filter.UserID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count clients: %w", err)
	}

	rows, err := r.db.Query(queryCtx, `
select c.id::text, c.name, c.email, c.phone, c.address, c.locality, c."createdAt", c."updatedAt"
from public."client" as c
where `+predicate+`
order by lower(coalesce(c.name, '')), c.id
limit $5 offset $6
`, filter.Search, filter.Role, filter.CompanyIDs, filter.UserID, filter.PageSize, (filter.Page-1)*filter.PageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("query clients: %w", err)
	}
	defer rows.Close()

	clients := make([]domain.Client, 0)
	for rows.Next() {
		client, err := scanClient(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan client: %w", err)
		}
		clients = append(clients, client)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read clients: %w", err)
	}
	return clients, total, nil
}

func (r *ClientRepository) GetClient(ctx context.Context, id string, filter outbound.ClientFilter) (domain.Client, []domain.ClientServiceRecord, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	client, err := scanClient(r.db.QueryRow(queryCtx, `
select c.id::text, c.name, c.email, c.phone, c.address, c.locality, c."createdAt", c."updatedAt"
from public."client" as c
where c.id = $1::uuid
  and ($2 <> 'TECHNICIAN' or exists (
      select 1
      from public."service" as assigned_service
      where assigned_service."clientId" = c.id
        and assigned_service."companyId"::text = any($3::text[])
        and assigned_service."technicianId"::text = $4
  ))
`, id, filter.Role, filter.CompanyIDs, filter.UserID))
	if err != nil {
		return domain.Client{}, nil, mapClientRepositoryError(err)
	}
	rows, err := r.db.Query(queryCtx, `
select id::text, status::text, "scheduledDate"
from public."service" as scoped_service
where "clientId" = $1::uuid
order by "scheduledDate" desc, id desc
limit 5
`, id)
	if err != nil {
		return domain.Client{}, nil, fmt.Errorf("query client services: %w", err)
	}
	defer rows.Close()

	services := make([]domain.ClientServiceRecord, 0)
	for rows.Next() {
		var service domain.ClientServiceRecord
		if err := rows.Scan(&service.ID, &service.Status, &service.ScheduledDate); err != nil {
			return domain.Client{}, nil, fmt.Errorf("scan client service: %w", err)
		}
		services = append(services, service)
	}
	if err := rows.Err(); err != nil {
		return domain.Client{}, nil, fmt.Errorf("read client services: %w", err)
	}
	return client, services, nil
}

func (r *ClientRepository) CreateClient(ctx context.Context, draft domain.ClientDraft) (domain.Client, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	client, err := scanClient(r.db.QueryRow(queryCtx, `
insert into public."client" (name, email, phone, address, locality)
values ($1, $2, $3, $4, $5)
returning id::text, name, email, phone, address, locality, "createdAt", "updatedAt"
`, draft.Name, draft.Email, draft.Phone, draft.Address, draft.Locality))
	if err != nil {
		return domain.Client{}, mapClientRepositoryError(err)
	}
	return client, nil
}

func (r *ClientRepository) UpdateClient(ctx context.Context, id string, patch domain.ClientPatch, filter outbound.ClientFilter) (domain.Client, error) {
	query, args, err := buildClientUpdateQuery(id, patch, filter)
	if err != nil {
		return domain.Client{}, err
	}

	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	client, err := scanClient(r.db.QueryRow(queryCtx, query, args...))
	if err != nil {
		return domain.Client{}, mapClientRepositoryError(err)
	}
	return client, nil
}

func buildClientUpdateQuery(id string, patch domain.ClientPatch, filter outbound.ClientFilter) (string, []any, error) {
	columns := []struct {
		name  string
		field domain.ClientField
	}{
		{name: "name", field: patch.Name},
		{name: "email", field: patch.Email},
		{name: "phone", field: patch.Phone},
		{name: "address", field: patch.Address},
		{name: "locality", field: patch.Locality},
	}
	sets := make([]string, 0, len(columns)+1)
	args := make([]any, 0, len(columns)+1)
	for _, column := range columns {
		if !column.field.Set {
			continue
		}
		if column.name == "phone" && column.field.Value == nil {
			return "", nil, ErrInvalidClientPatch
		}
		args = append(args, column.field.Value)
		sets = append(sets, fmt.Sprintf("%q = $%d", column.name, len(args)))
	}
	if len(sets) == 0 {
		return "", nil, ErrInvalidClientPatch
	}
	sets = append(sets, `"updatedAt" = now()`)
	args = append(args, id)
	idParameter := len(args)
	args = append(args, filter.Role, filter.CompanyIDs, filter.UserID)
	query := fmt.Sprintf(`
update public."client" as c
set %s
where c.id = $%d::uuid
  and ($%d <> 'TECHNICIAN' or exists (
      select 1
      from public."service" as assigned_service
      where assigned_service."clientId" = c.id
        and assigned_service."companyId"::text = any($%d::text[])
        and assigned_service."technicianId"::text = $%d
  ))
returning c.id::text, c.name, c.email, c.phone, c.address, c.locality, c."createdAt", c."updatedAt"
`, strings.Join(sets, ", "), idParameter, idParameter+1, idParameter+2, idParameter+3)
	return query, args, nil
}

func scanClient(row pgx.Row) (domain.Client, error) {
	var client domain.Client
	var name, email, address, locality pgtype.Text
	if err := row.Scan(
		&client.ID, &name, &email, &client.Phone,
		&address, &locality, &client.CreatedAt, &client.UpdatedAt,
	); err != nil {
		return domain.Client{}, err
	}
	client.Name = textPointer(name)
	client.Email = textPointer(email)
	client.Address = textPointer(address)
	client.Locality = textPointer(locality)
	return client, nil
}

func textPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func mapClientRepositoryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return outbound.ErrRecordNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return outbound.ErrConflict
	}
	return fmt.Errorf("client repository operation: %w", err)
}

func (r *ClientRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.ClientRepository = (*ClientRepository)(nil)
