package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type catalogAdminDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type CatalogAdminRepository struct {
	db           catalogAdminDB
	queryTimeout time.Duration
}

func NewCatalogAdminRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *CatalogAdminRepository {
	return &CatalogAdminRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *CatalogAdminRepository) ListProviders(ctx context.Context, filter domain.CatalogProviderFilter) (domain.CatalogProviderPage, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	const predicate = `p."companyId"::text = any($1::text[])
  and ($2 = '' or p."companyId"::text = $2)
  and ($3 = '' or p.name ilike '%' || $3 || '%')`
	var total int
	if err := r.db.QueryRow(queryCtx, `select count(*) from public."catalogProvider" as p where `+predicate,
		filter.Actor.CompanyIDs, filter.CompanyID, filter.Search).Scan(&total); err != nil {
		return domain.CatalogProviderPage{}, fmt.Errorf("count catalog providers: %w", err)
	}
	rows, err := r.db.Query(queryCtx, `
select p.id::text, p."companyId"::text, p.name, p."isActive", p."createdAt", p."updatedAt"
from public."catalogProvider" as p
where `+predicate+`
order by p.name asc, p.id asc
limit $4 offset $5`, filter.Actor.CompanyIDs, filter.CompanyID, filter.Search,
		filter.PageSize, (filter.Page-1)*filter.PageSize)
	if err != nil {
		return domain.CatalogProviderPage{}, fmt.Errorf("query catalog providers: %w", err)
	}
	defer rows.Close()
	providers := make([]domain.CatalogProvider, 0)
	for rows.Next() {
		var provider domain.CatalogProvider
		if err := rows.Scan(&provider.ID, &provider.CompanyID, &provider.Name, &provider.IsActive, &provider.CreatedAt, &provider.UpdatedAt); err != nil {
			return domain.CatalogProviderPage{}, fmt.Errorf("scan catalog provider: %w", err)
		}
		providers = append(providers, provider)
	}
	if err := rows.Err(); err != nil {
		return domain.CatalogProviderPage{}, fmt.Errorf("read catalog providers: %w", err)
	}
	return domain.CatalogProviderPage{Data: providers, Page: filter.Page, PageSize: filter.PageSize, Total: total}, nil
}

func (r *CatalogAdminRepository) CreateProvider(ctx context.Context, draft domain.CatalogProviderDraft, actor domain.ServiceActor) (domain.CatalogProvider, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	var provider domain.CatalogProvider
	err := r.db.QueryRow(queryCtx, `
insert into public."catalogProvider" ("companyId", name)
select $1::uuid, $2
where exists (
  select 1 from public."company" c where c.id = $1::uuid and c.id::text = any($3::text[])
)
returning id::text, "companyId"::text, name, "isActive", "createdAt", "updatedAt"`,
		draft.CompanyID, draft.Name, actor.CompanyIDs).Scan(
		&provider.ID, &provider.CompanyID, &provider.Name, &provider.IsActive, &provider.CreatedAt, &provider.UpdatedAt)
	if err != nil {
		return domain.CatalogProvider{}, mapCatalogAdminError(err)
	}
	return provider, nil
}

func (r *CatalogAdminRepository) UpdateProvider(ctx context.Context, id string, patch domain.CatalogProviderPatch, actor domain.ServiceActor) (domain.CatalogProvider, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	var name *string
	if patch.Name.Set {
		name = patch.Name.Value
	}
	var active *bool
	if patch.IsActive.Set {
		active = patch.IsActive.Value
	}
	var provider domain.CatalogProvider
	err := r.db.QueryRow(queryCtx, `
update public."catalogProvider"
set name = coalesce($2, name),
    "isActive" = coalesce($3, "isActive"),
    "updatedAt" = now()
where id = $1::uuid and "companyId"::text = any($4::text[])
returning id::text, "companyId"::text, name, "isActive", "createdAt", "updatedAt"`,
		id, name, active, actor.CompanyIDs).Scan(
		&provider.ID, &provider.CompanyID, &provider.Name, &provider.IsActive, &provider.CreatedAt, &provider.UpdatedAt)
	if err != nil {
		return domain.CatalogProvider{}, mapCatalogAdminError(err)
	}
	return provider, nil
}

func (r *CatalogAdminRepository) ListOffers(ctx context.Context, filter domain.CatalogOfferFilter) (domain.CatalogOfferPage, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	const predicate = `o."companyId"::text = any($1::text[])
  and ($2 = '' or o."companyId"::text = $2)
  and ($3 = '' or o."inventoryItemId"::text = $3)`
	var total int
	if err := r.db.QueryRow(queryCtx, `select count(*) from public."catalogOffer" as o where `+predicate,
		filter.Actor.CompanyIDs, filter.CompanyID, filter.InventoryItemID).Scan(&total); err != nil {
		return domain.CatalogOfferPage{}, fmt.Errorf("count catalog offers: %w", err)
	}
	rows, err := r.db.Query(queryCtx, `
select o.id::text, o."companyId"::text, o."inventoryItemId"::text, i.code, i.name,
       o."providerId"::text, p.name, o."costUsd"::text, o."isActive", o."createdAt", o."updatedAt"
from public."catalogOffer" as o
join public."inventoryItem" as i on i.id = o."inventoryItemId"
join public."catalogProvider" as p on p.id = o."providerId"
where `+predicate+`
order by i.name asc, o."createdAt" desc, o.id asc
limit $4 offset $5`, filter.Actor.CompanyIDs, filter.CompanyID, filter.InventoryItemID,
		filter.PageSize, (filter.Page-1)*filter.PageSize)
	if err != nil {
		return domain.CatalogOfferPage{}, fmt.Errorf("query catalog offers: %w", err)
	}
	defer rows.Close()
	offers := make([]domain.CatalogOffer, 0)
	for rows.Next() {
		offer, err := scanCatalogOffer(rows)
		if err != nil {
			return domain.CatalogOfferPage{}, fmt.Errorf("scan catalog offer: %w", err)
		}
		offers = append(offers, offer)
	}
	if err := rows.Err(); err != nil {
		return domain.CatalogOfferPage{}, fmt.Errorf("read catalog offers: %w", err)
	}
	return domain.CatalogOfferPage{Data: offers, Page: filter.Page, PageSize: filter.PageSize, Total: total}, nil
}

func (r *CatalogAdminRepository) CreateOffer(ctx context.Context, draft domain.CatalogOfferDraft, actor domain.ServiceActor) (domain.CatalogOffer, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.CatalogOffer{}, fmt.Errorf("begin catalog offer creation: %w", err)
	}
	defer rollback(tx)

	var itemCompany, itemCode, itemName string
	if err := tx.QueryRow(queryCtx, `
select i."companyId"::text, i.code, i.name
from public."inventoryItem" as i
where i.id = $1::uuid and i."companyId"::text = any($2::text[])
for update`, draft.InventoryItemID, actor.CompanyIDs).Scan(&itemCompany, &itemCode, &itemName); err != nil {
		return domain.CatalogOffer{}, mapCatalogAdminError(err)
	}
	if itemCompany != draft.CompanyID {
		return domain.CatalogOffer{}, outbound.ErrRuleViolation
	}
	var providerCompany, providerName string
	var providerActive bool
	if err := tx.QueryRow(queryCtx, `
select p."companyId"::text, p.name, p."isActive"
from public."catalogProvider" as p
where p.id = $1::uuid and p."companyId"::text = any($2::text[])`,
		draft.ProviderID, actor.CompanyIDs).Scan(&providerCompany, &providerName, &providerActive); err != nil {
		return domain.CatalogOffer{}, mapCatalogAdminError(err)
	}
	if providerCompany != draft.CompanyID || !providerActive {
		return domain.CatalogOffer{}, outbound.ErrRuleViolation
	}
	fxRate, err := latestExchangeRateValue(queryCtx, tx)
	if err != nil {
		return domain.CatalogOffer{}, err
	}
	if _, err := tx.Exec(queryCtx, `
update public."catalogOffer" set "isActive" = false, "updatedAt" = now()
where "inventoryItemId" = $1::uuid and "isActive"`, draft.InventoryItemID); err != nil {
		return domain.CatalogOffer{}, fmt.Errorf("deactivate previous catalog offer: %w", err)
	}
	var offer domain.CatalogOffer
	if err := tx.QueryRow(queryCtx, `
insert into public."catalogOffer" ("companyId", "inventoryItemId", "providerId", "costUsd")
values ($1::uuid, $2::uuid, $3::uuid, $4::numeric)
returning id::text, "companyId"::text, "inventoryItemId"::text, "providerId"::text, "costUsd"::text,
          "isActive", "createdAt", "updatedAt"`,
		draft.CompanyID, draft.InventoryItemID, draft.ProviderID, draft.CostUsd).Scan(
		&offer.ID, &offer.CompanyID, &offer.InventoryItemID, &offer.ProviderID, &offer.CostUsd,
		&offer.IsActive, &offer.CreatedAt, &offer.UpdatedAt); err != nil {
		return domain.CatalogOffer{}, mapCatalogAdminError(err)
	}
	offer.ItemCode, offer.ItemName, offer.ProviderName = itemCode, itemName, providerName
	if err := recalculateItemPrice(queryCtx, tx, draft.InventoryItemID, draft.CostUsd, fxRate); err != nil {
		return domain.CatalogOffer{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.CatalogOffer{}, fmt.Errorf("commit catalog offer creation: %w", err)
	}
	return offer, nil
}

func (r *CatalogAdminRepository) UpdateOffer(ctx context.Context, id string, patch domain.CatalogOfferPatch, actor domain.ServiceActor) (domain.CatalogOffer, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.CatalogOffer{}, fmt.Errorf("begin catalog offer update: %w", err)
	}
	defer rollback(tx)

	var offer domain.CatalogOffer
	var itemCode, itemName, providerName string
	if err := tx.QueryRow(queryCtx, `
select o.id::text, o."companyId"::text, o."inventoryItemId"::text, i.code, i.name,
       o."providerId"::text, p.name, o."costUsd"::text, o."isActive", o."createdAt", o."updatedAt"
from public."catalogOffer" as o
join public."inventoryItem" as i on i.id = o."inventoryItemId"
join public."catalogProvider" as p on p.id = o."providerId"
where o.id = $1::uuid and o."companyId"::text = any($2::text[])
for update of o`, id, actor.CompanyIDs).Scan(
		&offer.ID, &offer.CompanyID, &offer.InventoryItemID, &itemCode, &itemName,
		&offer.ProviderID, &providerName, &offer.CostUsd, &offer.IsActive, &offer.CreatedAt, &offer.UpdatedAt); err != nil {
		return domain.CatalogOffer{}, mapCatalogAdminError(err)
	}
	offer.ItemCode, offer.ItemName, offer.ProviderName = itemCode, itemName, providerName

	nextCost := offer.CostUsd
	if patch.CostUsd.Set {
		nextCost = *patch.CostUsd.Value
	}
	nextActive := offer.IsActive
	if patch.IsActive.Set {
		nextActive = *patch.IsActive.Value
	}
	if nextActive && !offer.IsActive {
		if _, err := tx.Exec(queryCtx, `
update public."catalogOffer" set "isActive" = false, "updatedAt" = now()
where "inventoryItemId" = $1::uuid and "isActive" and id <> $2::uuid`,
			offer.InventoryItemID, offer.ID); err != nil {
			return domain.CatalogOffer{}, fmt.Errorf("deactivate competing catalog offers: %w", err)
		}
	}
	if err := tx.QueryRow(queryCtx, `
update public."catalogOffer"
set "costUsd" = $2::numeric, "isActive" = $3, "updatedAt" = now()
where id = $1::uuid
returning id::text, "companyId"::text, "inventoryItemId"::text, "providerId"::text, "costUsd"::text,
          "isActive", "createdAt", "updatedAt"`, offer.ID, nextCost, nextActive).Scan(
		&offer.ID, &offer.CompanyID, &offer.InventoryItemID, &offer.ProviderID, &offer.CostUsd,
		&offer.IsActive, &offer.CreatedAt, &offer.UpdatedAt); err != nil {
		return domain.CatalogOffer{}, mapCatalogAdminError(err)
	}
	if nextActive && (patch.CostUsd.Set || patch.IsActive.Set) {
		fxRate, err := latestExchangeRateValue(queryCtx, tx)
		if err != nil {
			return domain.CatalogOffer{}, err
		}
		if err := recalculateItemPrice(queryCtx, tx, offer.InventoryItemID, offer.CostUsd, fxRate); err != nil {
			return domain.CatalogOffer{}, err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.CatalogOffer{}, fmt.Errorf("commit catalog offer update: %w", err)
	}
	offer.ItemCode, offer.ItemName, offer.ProviderName = itemCode, itemName, providerName
	return offer, nil
}

func (r *CatalogAdminRepository) LatestExchangeRate(ctx context.Context) (domain.ExchangeRate, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	var rate domain.ExchangeRate
	if err := r.db.QueryRow(queryCtx, `
select id::text, "rateDate"::text, rate::text, "createdById"::text, "createdAt", "updatedAt"
from public."exchangeRate"
order by "rateDate" desc
limit 1`).Scan(&rate.ID, &rate.RateDate, &rate.Rate, &rate.CreatedByID, &rate.CreatedAt, &rate.UpdatedAt); err != nil {
		return domain.ExchangeRate{}, mapCatalogAdminError(err)
	}
	return rate, nil
}

func (r *CatalogAdminRepository) UpsertExchangeRate(ctx context.Context, draft domain.ExchangeRateDraft, actor domain.ServiceActor) (domain.ExchangeRate, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	var rate domain.ExchangeRate
	if err := r.db.QueryRow(queryCtx, `
insert into public."exchangeRate" ("rateDate", rate, "createdById")
values ($1::date, $2::numeric, $3::uuid)
on conflict ("rateDate") do update
set rate = excluded.rate, "createdById" = excluded."createdById", "updatedAt" = now()
returning id::text, "rateDate"::text, rate::text, "createdById"::text, "createdAt", "updatedAt"`,
		draft.RateDate, draft.Rate, actor.UserID).Scan(
		&rate.ID, &rate.RateDate, &rate.Rate, &rate.CreatedByID, &rate.CreatedAt, &rate.UpdatedAt); err != nil {
		return domain.ExchangeRate{}, mapCatalogAdminError(err)
	}
	return rate, nil
}

func (r *CatalogAdminRepository) UpdateItemPrice(ctx context.Context, itemID string, patch domain.CatalogItemPatch, actor domain.ServiceActor) (domain.CatalogItemPrice, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	var margin, techPrice *string
	if patch.MarginPercent.Set {
		margin = patch.MarginPercent.Value
	}
	if patch.TechPrice.Set {
		techPrice = patch.TechPrice.Value
	}
	var active *bool
	if patch.IsActive.Set {
		active = patch.IsActive.Value
	}
	var item domain.CatalogItemPrice
	if err := r.db.QueryRow(queryCtx, `
update public."inventoryItem" as i
set "marginPercent" = coalesce($2::numeric, i."marginPercent"),
    "sellPrice" = case when $2::numeric is not null
                       then round(i."costPrice" * (1 + $2::numeric / 100), 2)
                       else i."sellPrice" end,
    "techPrice" = case when $3::numeric is not null then $3::numeric
                       when $2::numeric is not null then round(i."costPrice" * (1 + $2::numeric / 100), 2)
                       else i."techPrice" end,
    "isActive" = coalesce($4::boolean, i."isActive"),
    "updatedAt" = now()
where i.id = $1::uuid and i."companyId"::text = any($5::text[])
returning i.id::text, i."companyId"::text, i.code, i.name, i."costInitList"::text,
          i."costPrice"::text, i."sellPrice"::text, i."techPrice"::text,
          i."marginPercent"::text, (i."sellPrice" - i."costPrice")::text,
          i."isActive", i."updatedAt"`,
		itemID, margin, techPrice, active, actor.CompanyIDs).Scan(
		&item.ID, &item.CompanyID, &item.Code, &item.Name, &item.CostInitList,
		&item.CostPrice, &item.SellPrice, &item.TechPrice, &item.MarginPercent,
		&item.Gain, &item.IsActive, &item.UpdatedAt); err != nil {
		return domain.CatalogItemPrice{}, mapCatalogAdminError(err)
	}
	return item, nil
}

func latestExchangeRateValue(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (string, error) {
	var rate string
	if err := q.QueryRow(ctx, `
select rate::text from public."exchangeRate"
order by "rateDate" desc
limit 1`).Scan(&rate); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", outbound.ErrRuleViolation
		}
		return "", fmt.Errorf("read exchange rate: %w", err)
	}
	return rate, nil
}

func recalculateItemPrice(ctx context.Context, tx pgx.Tx, itemID, costUsd, fxRate string) error {
	if _, err := tx.Exec(ctx, `
update public."inventoryItem" as i
set "costInitList" = round($2::numeric * $3::numeric, 2),
    "costPrice" = round($2::numeric * $3::numeric * (1 + c."costMarkupPercent" / 100), 2),
    "sellPrice" = round(round($2::numeric * $3::numeric * (1 + c."costMarkupPercent" / 100), 2) * (1 + i."marginPercent" / 100), 2),
    "techPrice" = round(round($2::numeric * $3::numeric * (1 + c."costMarkupPercent" / 100), 2) * (1 + i."marginPercent" / 100), 2),
    "importedAt" = now(), "updatedAt" = now()
from public."company" as c
where i.id = $1::uuid and c.id = i."companyId"`, itemID, costUsd, fxRate); err != nil {
		return fmt.Errorf("recalculate inventory item price: %w", err)
	}
	return nil
}

func scanCatalogOffer(row pgx.Row) (domain.CatalogOffer, error) {
	var offer domain.CatalogOffer
	if err := row.Scan(&offer.ID, &offer.CompanyID, &offer.InventoryItemID, &offer.ItemCode, &offer.ItemName,
		&offer.ProviderID, &offer.ProviderName, &offer.CostUsd, &offer.IsActive, &offer.CreatedAt, &offer.UpdatedAt); err != nil {
		return domain.CatalogOffer{}, err
	}
	return offer, nil
}

func mapCatalogAdminError(err error) error {
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
		case "42P01":
			return outbound.ErrRuleViolation
		}
	}
	return err
}

func (r *CatalogAdminRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.CatalogAdminRepository = (*CatalogAdminRepository)(nil)
