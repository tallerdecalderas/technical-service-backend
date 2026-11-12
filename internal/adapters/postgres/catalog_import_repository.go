package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type catalogImportDB interface {
	Begin(context.Context) (pgx.Tx, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type CatalogImportRepository struct {
	db           catalogImportDB
	queryTimeout time.Duration
}

func NewCatalogImportRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *CatalogImportRepository {
	return &CatalogImportRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *CatalogImportRepository) PublishCatalog(ctx context.Context, input domain.CatalogImportPublishInput) (domain.CatalogImport, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.CatalogImport{}, fmt.Errorf("begin catalog import: %w", err)
	}
	defer rollback(tx)

	if _, err := tx.Exec(queryCtx, `select pg_advisory_xact_lock(hashtext($1))`, input.CompanyID); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("lock catalog import: %w", err)
	}
	var fxRate string
	if err := tx.QueryRow(queryCtx, `
select rate::text from public."exchangeRate"
order by "rateDate" desc
limit 1`).Scan(&fxRate); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CatalogImport{}, outbound.ErrRuleViolation
		}
		return domain.CatalogImport{}, fmt.Errorf("read catalog import exchange rate: %w", err)
	}
	var companyExists bool
	if err := tx.QueryRow(queryCtx, `select exists (select 1 from public."company" where id = $1::uuid)`, input.CompanyID).Scan(&companyExists); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("verify catalog import company: %w", err)
	}
	if !companyExists {
		return domain.CatalogImport{}, outbound.ErrRecordNotFound
	}
	if _, err := tx.Exec(queryCtx, `
update public."catalogImport" set "isActive" = false, "updatedAt" = now()
where "companyId" = $1::uuid and "isActive"`, input.CompanyID); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("deactivate previous catalog import: %w", err)
	}
	var importID string
	var startedAt time.Time
	if err := tx.QueryRow(queryCtx, `
insert into public."catalogImport" ("companyId", "startedById", status, "sourceRef", "rowCount", "itemCount", "errorCount", errors, "startedAt")
values ($1::uuid, $2::uuid, 'PUBLISHED', $3, $4, 0, 0, '[]'::jsonb, now())
returning id::text, "startedAt"`, input.CompanyID, input.StartedByID, input.SourceRef, len(input.Rows)).Scan(&importID, &startedAt); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("create catalog import: %w", err)
	}

	providerIDs := map[string]string{}
	itemIDs := map[string]string{}
	codes := make([]string, 0, len(input.Rows))
	for _, row := range input.Rows {
		providerKey := normalizeImportKey(row.ProviderName)
		providerID, ok := providerIDs[providerKey]
		if !ok {
			if err := tx.QueryRow(queryCtx, `
insert into public."catalogProvider" ("companyId", name, "isActive")
values ($1::uuid, $2, true)
on conflict ("companyId", name) do update set "isActive" = true, "updatedAt" = now()
returning id::text`, input.CompanyID, row.ProviderName).Scan(&providerID); err != nil {
				return domain.CatalogImport{}, fmt.Errorf("upsert catalog provider: %w", err)
			}
			providerIDs[providerKey] = providerID
		}
		var itemID string
		if err := tx.QueryRow(queryCtx, `
insert into public."inventoryItem" as i
  ("companyId", code, name, unit, "costInitList", "costPrice", "sellPrice", "techPrice", "marginPercent", "isActive", "importedAt", "updatedAt")
select $1::uuid, $2, $3, $4,
       round($5::numeric * $6::numeric, 2),
       round($5::numeric * $6::numeric * (1 + c."costMarkupPercent" / 100), 2),
       round(round($5::numeric * $6::numeric * (1 + c."costMarkupPercent" / 100), 2) * (1 + $7::numeric / 100), 2),
       round(round($5::numeric * $6::numeric * (1 + c."costMarkupPercent" / 100), 2) * (1 + $7::numeric / 100), 2),
       $7::numeric, true, now(), now()
from public."company" as c
where c.id = $1::uuid
on conflict ("companyId", code) do update set
  name = excluded.name,
  unit = excluded.unit,
  "costInitList" = excluded."costInitList",
  "costPrice" = excluded."costPrice",
  "sellPrice" = excluded."sellPrice",
  "techPrice" = excluded."techPrice",
  "marginPercent" = excluded."marginPercent",
  "isActive" = true,
  "importedAt" = now(),
  "updatedAt" = now()
returning i.id::text`,
			input.CompanyID, row.Code, row.Name, row.Unit, row.CostUsd, fxRate, row.MarginPercent).Scan(&itemID); err != nil {
			return domain.CatalogImport{}, fmt.Errorf("upsert catalog item: %w", err)
		}
		itemIDs[row.Code] = itemID
		codes = append(codes, row.Code)
		if _, err := tx.Exec(queryCtx, `
update public."catalogOffer" set "isActive" = false, "updatedAt" = now()
where "inventoryItemId" = $1::uuid and "isActive"`, itemID); err != nil {
			return domain.CatalogImport{}, fmt.Errorf("deactivate previous catalog offer: %w", err)
		}
		if _, err := tx.Exec(queryCtx, `
insert into public."catalogOffer" ("companyId", "inventoryItemId", "providerId", "costUsd", "isActive")
values ($1::uuid, $2::uuid, $3::uuid, $4::numeric, true)`,
			input.CompanyID, itemID, providerID, row.CostUsd); err != nil {
			return domain.CatalogImport{}, fmt.Errorf("create catalog offer: %w", err)
		}
	}
	if _, err := tx.Exec(queryCtx, `
update public."inventoryItem" set "isActive" = false, "updatedAt" = now()
where "companyId" = $1::uuid and not (code = any($2::text[]))`, input.CompanyID, codes); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("deactivate missing catalog items: %w", err)
	}
	var imported domain.CatalogImport
	if err := tx.QueryRow(queryCtx, `
update public."catalogImport"
set "itemCount" = $2, "isActive" = true, "finishedAt" = now(), "updatedAt" = now()
where id = $1::uuid
returning id::text, "companyId"::text, "startedById"::text, status, coalesce("sourceRef", ''),
          "rowCount", "itemCount", "errorCount", errors, "startedAt", "finishedAt"`,
		importID, len(itemIDs)).Scan(
		&imported.ID, &imported.CompanyID, &imported.StartedByID, &imported.Status, &imported.SourceRef,
		&imported.RowCount, &imported.ItemCount, &imported.ErrorCount, &imported.Errors,
		&imported.StartedAt, &imported.FinishedAt); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("finish catalog import: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("commit catalog import: %w", err)
	}
	return imported, nil
}

func (r *CatalogImportRepository) RecordFailedCatalogImport(ctx context.Context, failure domain.CatalogImportFailure) (domain.CatalogImport, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	errorsJSON, err := json.Marshal(failure.Errors)
	if err != nil {
		return domain.CatalogImport{}, fmt.Errorf("encode catalog import errors: %w", err)
	}
	var imported domain.CatalogImport
	if err := r.db.QueryRow(queryCtx, `
insert into public."catalogImport"
  ("companyId", "startedById", status, "sourceRef", "rowCount", "itemCount", "errorCount", errors, "startedAt", "finishedAt")
values ($1::uuid, $2::uuid, 'FAILED', $3, $4, 0, $5, $6::jsonb, now(), now())
returning id::text, "companyId"::text, "startedById"::text, status, coalesce("sourceRef", ''),
          "rowCount", "itemCount", "errorCount", errors, "startedAt", "finishedAt"`,
		failure.CompanyID, failure.StartedByID, failure.SourceRef, failure.RowCount,
		len(failure.Errors), errorsJSON).Scan(
		&imported.ID, &imported.CompanyID, &imported.StartedByID, &imported.Status, &imported.SourceRef,
		&imported.RowCount, &imported.ItemCount, &imported.ErrorCount, &imported.Errors,
		&imported.StartedAt, &imported.FinishedAt); err != nil {
		return domain.CatalogImport{}, fmt.Errorf("record failed catalog import: %w", err)
	}
	return imported, nil
}

func normalizeImportKey(value string) string {
	lowered := make([]rune, 0, len(value))
	for _, char := range value {
		switch char {
		case ' ':
			continue
		default:
			if char >= 'A' && char <= 'Z' {
				char += 'a' - 'A'
			}
			lowered = append(lowered, char)
		}
	}
	return string(lowered)
}

func (r *CatalogImportRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.CatalogImportRepository = (*CatalogImportRepository)(nil)
