package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type companyDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
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
		if err := rows.Scan(&company.ID, &company.Name); err != nil {
			return nil, fmt.Errorf("scan authorized company: %w", err)
		}
		companies = append(companies, company)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read authorized companies: %w", err)
	}
	return companies, nil
}

func (r *CompanyRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.CompanyRepository = (*CompanyRepository)(nil)
