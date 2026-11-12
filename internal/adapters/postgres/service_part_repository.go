package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type servicePartDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type ServicePartRepository struct {
	db           servicePartDB
	queryTimeout time.Duration
}

func NewServicePartRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *ServicePartRepository {
	return &ServicePartRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *ServicePartRepository) ListServiceParts(ctx context.Context, serviceID string, actor domain.ServiceActor) ([]domain.ServicePart, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	if err := r.ensureVisibleService(queryCtx, r.db, serviceID, actor); err != nil {
		return nil, err
	}
	rows, err := r.db.Query(queryCtx, `
select p.id::text, p."serviceId"::text, p."inventoryItemId"::text, p.name, p.nota,
       p."providerName", p."costUsd"::text, p."exchangeRate"::text,
       p.quantity::text, p."unitCost"::text, p."totalCost"::text,
       p."unitSalePrice"::text, p."totalSalePrice"::text, p."createdAt", p."updatedAt"
from public."servicePart" as p
where p."serviceId" = $1::uuid
order by p."createdAt" asc, p.id asc
`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("query service parts: %w", err)
	}
	defer rows.Close()
	parts := make([]domain.ServicePart, 0)
	for rows.Next() {
		part, err := scanServicePart(rows)
		if err != nil {
			return nil, fmt.Errorf("scan service part: %w", err)
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read service parts: %w", err)
	}
	return parts, nil
}

func (r *ServicePartRepository) CreateServicePart(ctx context.Context, draft domain.ServicePartDraft, actor domain.ServiceActor) (domain.ServicePart, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("begin service part creation: %w", err)
	}
	defer rollback(tx)
	companyID, err := r.lockScopedService(queryCtx, tx, draft.ServiceID, actor)
	if err != nil {
		return domain.ServicePart{}, err
	}
	if err := r.ensureServiceNotLiquidated(queryCtx, tx, draft.ServiceID); err != nil {
		return domain.ServicePart{}, err
	}
	if draft.InventoryItemID != nil {
		if err := r.snapshotInventoryItem(queryCtx, tx, *draft.InventoryItemID, companyID, &draft); err != nil {
			return domain.ServicePart{}, err
		}
	}
	totalCost, err := multiplyDecimal(draft.Quantity, draft.UnitCost)
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("calculate service part cost: %w", err)
	}
	totalSalePrice, err := multiplyDecimal(draft.Quantity, draft.UnitSalePrice)
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("calculate service part sale price: %w", err)
	}
	part, err := scanServicePart(tx.QueryRow(queryCtx, `
insert into public."servicePart"
    ("serviceId", "inventoryItemId", name, nota, "providerName", "costUsd", "exchangeRate",
     quantity, "unitCost", "totalCost", "unitSalePrice", "totalSalePrice")
values ($1::uuid, $2::uuid, $3, $4, $5, $6::numeric, $7::numeric,
        $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric)
returning id::text, "serviceId"::text, "inventoryItemId"::text, name, nota,
          "providerName", "costUsd"::text, "exchangeRate"::text,
          quantity::text, "unitCost"::text, "totalCost"::text,
          "unitSalePrice"::text, "totalSalePrice"::text, "createdAt", "updatedAt"
`, draft.ServiceID, draft.InventoryItemID, draft.Name, draft.Nota, draft.ProviderName, draft.CostUsd, draft.ExchangeRate,
		draft.Quantity, draft.UnitCost, totalCost, draft.UnitSalePrice, totalSalePrice))
	if err != nil {
		return domain.ServicePart{}, mapServicePartRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]any{"partId": part.ID, "inventoryItemId": part.InventoryItemID, "next": servicePartAuditValues(part)})
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("encode service part creation audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: draft.ServiceID, UserID: actor.UserID, Action: "PART_ADDED",
		Description: "Repuesto agregado: " + part.Name, Metadata: metadata,
	}); err != nil {
		return domain.ServicePart{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.ServicePart{}, fmt.Errorf("commit service part creation: %w", err)
	}
	return part, nil
}

func (r *ServicePartRepository) UpdateServicePart(ctx context.Context, serviceID, partID string, patch domain.ServicePartPatch, actor domain.ServiceActor) (domain.ServicePart, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("begin service part update: %w", err)
	}
	defer rollback(tx)
	if _, err := r.lockScopedService(queryCtx, tx, serviceID, actor); err != nil {
		return domain.ServicePart{}, err
	}
	if err := r.ensureServiceNotLiquidated(queryCtx, tx, serviceID); err != nil {
		return domain.ServicePart{}, err
	}
	current, err := r.lockServicePart(queryCtx, tx, serviceID, partID)
	if err != nil {
		return domain.ServicePart{}, err
	}
	updatedDraft := applyServicePartPatch(current, patch)
	totalCost, err := multiplyDecimal(updatedDraft.Quantity, updatedDraft.UnitCost)
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("calculate updated service part cost: %w", err)
	}
	totalSalePrice, err := multiplyDecimal(updatedDraft.Quantity, updatedDraft.UnitSalePrice)
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("calculate updated service part sale price: %w", err)
	}
	updated, err := scanServicePart(tx.QueryRow(queryCtx, `
update public."servicePart"
set name = $3, nota = $4, quantity = $5::numeric, "unitCost" = $6::numeric,
    "totalCost" = $7::numeric, "unitSalePrice" = $8::numeric,
    "totalSalePrice" = $9::numeric, "updatedAt" = now()
where id = $1::uuid and "serviceId" = $2::uuid
returning id::text, "serviceId"::text, "inventoryItemId"::text, name, nota,
          "providerName", "costUsd"::text, "exchangeRate"::text,
          quantity::text, "unitCost"::text, "totalCost"::text,
          "unitSalePrice"::text, "totalSalePrice"::text, "createdAt", "updatedAt"
`, partID, serviceID, updatedDraft.Name, updatedDraft.Nota, updatedDraft.Quantity, updatedDraft.UnitCost, totalCost, updatedDraft.UnitSalePrice, totalSalePrice))
	if err != nil {
		return domain.ServicePart{}, mapServicePartRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]any{"partId": updated.ID, "previous": servicePartAuditValues(current), "next": servicePartAuditValues(updated)})
	if err != nil {
		return domain.ServicePart{}, fmt.Errorf("encode service part update audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: serviceID, UserID: actor.UserID, Action: "PART_UPDATED",
		Description: "Repuesto actualizado: " + updated.Name, Metadata: metadata,
	}); err != nil {
		return domain.ServicePart{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.ServicePart{}, fmt.Errorf("commit service part update: %w", err)
	}
	return updated, nil
}

func (r *ServicePartRepository) DeleteServicePart(ctx context.Context, serviceID, partID string, actor domain.ServiceActor) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return fmt.Errorf("begin service part deletion: %w", err)
	}
	defer rollback(tx)
	if _, err := r.lockScopedService(queryCtx, tx, serviceID, actor); err != nil {
		return err
	}
	if err := r.ensureServiceNotLiquidated(queryCtx, tx, serviceID); err != nil {
		return err
	}
	part, err := r.lockServicePart(queryCtx, tx, serviceID, partID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(queryCtx, `delete from public."servicePart" where id = $1::uuid and "serviceId" = $2::uuid`, partID, serviceID); err != nil {
		return mapServicePartRepositoryError(err)
	}
	metadata, err := json.Marshal(map[string]any{"partId": part.ID, "previous": servicePartAuditValues(part)})
	if err != nil {
		return fmt.Errorf("encode service part deletion audit: %w", err)
	}
	if err := insertActivity(queryCtx, tx, domain.ActivityRecord{
		ServiceID: serviceID, UserID: actor.UserID, Action: "PART_REMOVED",
		Description: "Repuesto retirado: " + part.Name, Metadata: metadata,
	}); err != nil {
		return err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return fmt.Errorf("commit service part deletion: %w", err)
	}
	return nil
}

type servicePartServiceQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *ServicePartRepository) ensureVisibleService(ctx context.Context, db servicePartServiceQuerier, serviceID string, actor domain.ServiceActor) error {
	var visible bool
	if err := db.QueryRow(ctx, `
select exists (
    select 1
    from public."service" as s
    where s.id = $1::uuid
      and s."companyId"::text = any($2::text[])
      and ($3 <> 'TECHNICIAN' or s."technicianId"::text = $4)
)
`, serviceID, actor.CompanyIDs, actor.Role, actor.UserID).Scan(&visible); err != nil {
		return fmt.Errorf("check service part visibility: %w", err)
	}
	if !visible {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *ServicePartRepository) lockScopedService(ctx context.Context, tx pgx.Tx, serviceID string, actor domain.ServiceActor) (string, error) {
	var companyID string
	if err := tx.QueryRow(ctx, `
select s."companyId"::text
from public."service" as s
where s.id = $1::uuid and s."companyId"::text = any($2::text[])
for update
`, serviceID, actor.CompanyIDs).Scan(&companyID); err != nil {
		return "", mapServicePartRepositoryError(err)
	}
	return companyID, nil
}

func (r *ServicePartRepository) ensureServiceNotLiquidated(ctx context.Context, tx pgx.Tx, serviceID string) error {
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

func (r *ServicePartRepository) snapshotInventoryItem(ctx context.Context, tx pgx.Tx, inventoryItemID, companyID string, draft *domain.ServicePartDraft) error {
	var providerName, costUsd, exchangeRate pgtype.Text
	if err := tx.QueryRow(ctx, `
select i.name, i."costPrice"::text, i."sellPrice"::text,
       p.name, o."costUsd"::text, fx.rate
from public."inventoryItem" as i
left join public."catalogOffer" as o on o."inventoryItemId" = i.id and o."isActive"
left join public."catalogProvider" as p on p.id = o."providerId"
left join lateral (
    select rate::text as rate
    from public."exchangeRate"
    order by "rateDate" desc
    limit 1
) as fx on true
where i.id = $1::uuid and i."companyId" = $2::uuid
`, inventoryItemID, companyID).Scan(&draft.Name, &draft.UnitCost, &draft.UnitSalePrice,
		&providerName, &costUsd, &exchangeRate); err != nil {
		return mapServicePartRepositoryError(err)
	}
	draft.ProviderName = textPointer(providerName)
	draft.CostUsd = textPointer(costUsd)
	draft.ExchangeRate = textPointer(exchangeRate)
	return nil
}

func (r *ServicePartRepository) lockServicePart(ctx context.Context, tx pgx.Tx, serviceID, partID string) (domain.ServicePart, error) {
	part, err := scanServicePart(tx.QueryRow(ctx, `
select p.id::text, p."serviceId"::text, p."inventoryItemId"::text, p.name, p.nota,
       p."providerName", p."costUsd"::text, p."exchangeRate"::text,
       p.quantity::text, p."unitCost"::text, p."totalCost"::text,
       p."unitSalePrice"::text, p."totalSalePrice"::text, p."createdAt", p."updatedAt"
from public."servicePart" as p
where p.id = $1::uuid and p."serviceId" = $2::uuid
for update
`, partID, serviceID))
	if err != nil {
		return domain.ServicePart{}, mapServicePartRepositoryError(err)
	}
	return part, nil
}

func applyServicePartPatch(part domain.ServicePart, patch domain.ServicePartPatch) domain.ServicePartDraft {
	draft := domain.ServicePartDraft{
		ServiceID: part.ServiceID, InventoryItemID: part.InventoryItemID, Name: part.Name, Nota: part.Nota,
		ProviderName: part.ProviderName, CostUsd: part.CostUsd, ExchangeRate: part.ExchangeRate,
		Quantity: part.Quantity, UnitCost: part.UnitCost, UnitSalePrice: part.UnitSalePrice,
	}
	if patch.Name.Set {
		draft.Name = *patch.Name.Value
	}
	if patch.Nota.Set {
		draft.Nota = patch.Nota.Value
	}
	if patch.Quantity.Set {
		draft.Quantity = *patch.Quantity.Value
	}
	if patch.UnitCost.Set {
		draft.UnitCost = *patch.UnitCost.Value
	}
	if patch.UnitSalePrice.Set {
		draft.UnitSalePrice = *patch.UnitSalePrice.Value
	}
	return draft
}

func multiplyDecimal(left, right string) (string, error) {
	leftValue, ok := new(big.Rat).SetString(left)
	if !ok {
		return "", errors.New("invalid left decimal")
	}
	rightValue, ok := new(big.Rat).SetString(right)
	if !ok {
		return "", errors.New("invalid right decimal")
	}
	value := new(big.Rat).Mul(leftValue, rightValue).FloatString(decimalScale(left) + decimalScale(right))
	if strings.Contains(value, ".") {
		value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
	}
	if value == "" || value == "-0" {
		return "0", nil
	}
	return value, nil
}

func decimalScale(value string) int {
	point := strings.LastIndexByte(value, '.')
	if point < 0 {
		return 0
	}
	return len(value) - point - 1
}

func scanServicePart(row pgx.Row) (domain.ServicePart, error) {
	var part domain.ServicePart
	var inventoryItemID, nota, providerName, costUsd, exchangeRate pgtype.Text
	if err := row.Scan(
		&part.ID, &part.ServiceID, &inventoryItemID, &part.Name, &nota,
		&providerName, &costUsd, &exchangeRate,
		&part.Quantity, &part.UnitCost, &part.TotalCost, &part.UnitSalePrice, &part.TotalSalePrice,
		&part.CreatedAt, &part.UpdatedAt,
	); err != nil {
		return domain.ServicePart{}, err
	}
	part.InventoryItemID = textPointer(inventoryItemID)
	part.Nota = textPointer(nota)
	part.ProviderName = textPointer(providerName)
	part.CostUsd = textPointer(costUsd)
	part.ExchangeRate = textPointer(exchangeRate)
	return part, nil
}

func servicePartAuditValues(part domain.ServicePart) map[string]any {
	return map[string]any{
		"inventoryItemId": part.InventoryItemID, "name": part.Name, "nota": part.Nota,
		"providerName": part.ProviderName, "costUsd": part.CostUsd, "exchangeRate": part.ExchangeRate,
		"quantity": part.Quantity, "unitCost": part.UnitCost, "totalCost": part.TotalCost,
		"unitSalePrice": part.UnitSalePrice, "totalSalePrice": part.TotalSalePrice,
	}
}

func mapServicePartRepositoryError(err error) error {
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
	return fmt.Errorf("service part repository operation: %w", err)
}

func (r *ServicePartRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.ServicePartRepository = (*ServicePartRepository)(nil)
