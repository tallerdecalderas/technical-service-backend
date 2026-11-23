package postgres

import (
	"context"
	"database/sql"
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

type companyDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type CompanyRepository struct {
	db           companyDB
	queryTimeout time.Duration
}

func NewCompanyRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *CompanyRepository {
	return &CompanyRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *CompanyRepository) ListActiveCompanies(ctx context.Context, companyIDs []string) ([]domain.CompanySummary, error) {
	if len(companyIDs) == 0 {
		return []domain.CompanySummary{}, nil
	}

	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	rows, err := r.db.Query(queryCtx, `
select c.id::text, c.name
from public."company" as c
where c.id::text = any($1::text[])
  and c."isActive" = true
order by lower(c.name), c.id
`, companyIDs)
	if err != nil {
		return nil, fmt.Errorf("query authorized companies: %w", err)
	}
	defer rows.Close()

	companies := make([]domain.CompanySummary, 0)
	for rows.Next() {
		var company domain.CompanySummary
		if err := rows.Scan(&company.ID, &company.Name, &company.Avatar); err != nil {
			return nil, fmt.Errorf("scan authorized company: %w", err)
		}
		companies = append(companies, company)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read authorized companies: %w", err)
	}
	return companies, nil
}

func (r *CompanyRepository) CreateCompany(ctx context.Context, input domain.CompanyCreateInput) (string, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	var id string
	if err := r.db.QueryRow(queryCtx, `
insert into public."company" (name, "isActive", address, phone, avatar)
values ($1, true, $2, $3, $4)
returning id::text
`, input.Name, input.Address, input.Phone, input.Avatar).Scan(&id); err != nil {
		return "", fmt.Errorf("create company: %w", err)
	}
	return id, nil
}

func (r *CompanyRepository) GetCompany(ctx context.Context, id string) (*domain.Company, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	var company domain.Company
	var address, phone sql.NullString
	var createdAt, updatedAt pgtype.Timestamptz

	if err := r.db.QueryRow(queryCtx, `
select c.id::text, c.name, c."isActive", c.address, c.phone, c."createdAt", c."updatedAt"
from public."company" as c
where c.id::text = $1
`, id).Scan(
		&company.ID, &company.Name, &company.IsActive, &address, &phone, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, outbound.ErrRecordNotFound
		}
		return nil, fmt.Errorf("get company: %w", err)
	}

	if address.Valid {
		company.Address = address.String
	}
	if phone.Valid {
		company.Phone = phone.String
	}
	if createdAt.Valid {
		company.CreatedAt = createdAt.Time
	}
	if updatedAt.Valid {
		company.UpdatedAt = updatedAt.Time
	}
	return &company, nil
}

func (r *CompanyRepository) UpdateCompany(ctx context.Context, id string, input domain.CompanyUpdateInput) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	result, err := r.db.Exec(queryCtx, `
update public."company"
set name = coalesce($2, name),
    address = coalesce($3, address),
    phone = coalesce($4, phone),
    avatar = coalesce($5, avatar),
    "isActive" = coalesce($6, "isActive"),
    "updatedAt" = now()
where id::text = $1
`, id, input.Name, input.Address, input.Phone, input.Avatar, input.IsActive)
	if err != nil {
		return fmt.Errorf("update company: %w", err)
	}
	if result.RowsAffected() == 0 {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *CompanyRepository) DeactivateCompany(ctx context.Context, id string) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	result, err := r.db.Exec(queryCtx, `
update public."company"
set "isActive" = false,
    "updatedAt" = now()
where id::text = $1
`, id)
	if err != nil {
		return fmt.Errorf("deactivate company: %w", err)
	}
	if result.RowsAffected() == 0 {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *CompanyRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.CompanyRepository = (*CompanyRepository)(nil)
