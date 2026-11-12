package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type catalogDB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type CatalogRepository struct {
	db           catalogDB
	queryTimeout time.Duration
}

func NewCatalogRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *CatalogRepository {
	return &CatalogRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *CatalogRepository) ListCatalogPrices(ctx context.Context, filter domain.CatalogFilter) (domain.CatalogPage, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	const predicate = `i."companyId"::text = any($1::text[])
  and ($2 = '' or i."companyId"::text = $2)
  and i."isActive"
  and ($3 = '' or i.name ilike '%' || $3 || '%' or i.code ilike '%' || $3 || '%')`

	var total int
	if err := r.db.QueryRow(queryCtx, `
select count(*)
from public."inventoryItem" as i
where `+predicate, filter.Actor.CompanyIDs, filter.CompanyID, filter.Search).Scan(&total); err != nil {
		return domain.CatalogPage{}, fmt.Errorf("count catalog prices: %w", err)
	}

	rows, err := r.db.Query(queryCtx, `
select i.id::text, i."companyId"::text, c.name, i.code, i.name, i.unit,
       i."techPrice"::text, i."costPrice"::text, i."sellPrice"::text,
       i."costInitList"::text, i."marginPercent"::text, i."isActive", i."updatedAt"
from public."inventoryItem" as i
join public."company" as c on c.id = i."companyId"
where `+predicate+`
order by i.name asc, i.code asc, i.id asc
limit $4 offset $5
`, filter.Actor.CompanyIDs, filter.CompanyID, filter.Search, filter.PageSize, (filter.Page-1)*filter.PageSize)
	if err != nil {
		return domain.CatalogPage{}, fmt.Errorf("query catalog prices: %w", err)
	}
	defer rows.Close()

	prices := make([]domain.CatalogPrice, 0)
	for rows.Next() {
		price, err := scanCatalogPrice(rows)
		if err != nil {
			return domain.CatalogPage{}, fmt.Errorf("scan catalog price: %w", err)
		}
		prices = append(prices, price)
	}
	if err := rows.Err(); err != nil {
		return domain.CatalogPage{}, fmt.Errorf("read catalog prices: %w", err)
	}
	rows.Close()

	version, updatedAt, err := r.catalogVersion(queryCtx, filter)
	if err != nil {
		return domain.CatalogPage{}, err
	}
	return domain.CatalogPage{
		Data: prices, Page: filter.Page, PageSize: filter.PageSize,
		Total: total, Version: version, UpdatedAt: updatedAt,
	}, nil
}

func (r *CatalogRepository) catalogVersion(ctx context.Context, filter domain.CatalogFilter) (string, time.Time, error) {
	var updatedAt time.Time
	var count int
	if err := r.db.QueryRow(ctx, `
select coalesce(max(i."updatedAt"), 'epoch'::timestamptz), count(*)
from public."inventoryItem" as i
where i."companyId"::text = any($1::text[])
  and ($2 = '' or i."companyId"::text = $2)
  and i."isActive"
`, filter.Actor.CompanyIDs, filter.CompanyID).Scan(&updatedAt, &count); err != nil {
		return "", time.Time{}, fmt.Errorf("read catalog version: %w", err)
	}
	updatedAt = updatedAt.UTC()
	return fmt.Sprintf("%s-%d", updatedAt.Format(time.RFC3339Nano), count), updatedAt, nil
}

func scanCatalogPrice(row pgx.Row) (domain.CatalogPrice, error) {
	var price domain.CatalogPrice
	var unit pgtype.Text
	if err := row.Scan(
		&price.ID, &price.CompanyID, &price.CompanyName, &price.Code, &price.Name, &unit,
		&price.TechPrice, &price.CostPrice, &price.SellPrice,
		&price.CostInitList, &price.MarginPercent, &price.IsActive, &price.UpdatedAt,
	); err != nil {
		return domain.CatalogPrice{}, err
	}
	price.Unit = textPointer(unit)
	return price, nil
}

func (r *CatalogRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.CatalogRepository = (*CatalogRepository)(nil)
