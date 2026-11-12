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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/money"
	"technical-service-backend/internal/ports/outbound"
)

type settlementDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type SettlementRepository struct {
	db           settlementDB
	queryTimeout time.Duration
}

func NewSettlementRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *SettlementRepository {
	return &SettlementRepository{db: pool, queryTimeout: queryTimeout}
}

const settlementVisibility = `(
  ($1 = 'TECHNICIAN' and s."technicianId" = $2::uuid)
  or ($1 = 'ADMIN' and (
    (s."companyId" is not null and s."companyId"::text = any($3::text[]))
    or (s."companyId" is null and not exists (
      select 1 from public."settlementItem" si
      join public."service" sv on sv.id = si."serviceId"
      where si."settlementId" = s.id and not (sv."companyId"::text = any($3::text[]))
    ))
  ))
)`

const settlementHeaderSelect = `
select s.id::text, s."technicianId"::text, u.name,
       s."companyId"::text, coalesce(c.name, ''),
       s."startDate", s."endDate", coalesce(s.label, ''),
       s.status, s."commissionRate"::text, coalesce(s."companyRate", 0.5)::text,
       s."servicesCount",
       s."totalCollected"::text, s."totalDebt"::text, s."totalPartsCost"::text, s."totalPartsSale"::text,
       coalesce(s."totalExpenses", 0)::text, coalesce(s."totalInsurance", 0)::text,
       coalesce(s."totalOtherAdjustments", 0)::text, coalesce(s."totalDiscounts", 0)::text,
       coalesce(s."totalAdvances", 0)::text, s."commissionBase"::text,
       s."techCommission"::text, s."compCommission"::text, coalesce(s."techBalance", 0)::text,
       s."liquidatedAt", s."liquidatedById"::text, s."createdById"::text,
       s."createdAt", s."updatedAt"
from public.settlement as s
join public."user" as u on u.id = s."technicianId"
left join public.company as c on c.id = s."companyId"`

func (r *SettlementRepository) ListSettlements(ctx context.Context, filter domain.SettlementFilter) (domain.SettlementPage, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	status := filter.Status
	if status == domain.SettlementStatusLiquidated {
		status = "PAID"
	}
	args := []any{
		filter.Actor.Role, filter.Actor.UserID, filter.Actor.CompanyIDs,
		filter.TechnicianID, filter.CompanyID, status, filter.DateFrom, filter.DateTo,
	}
	var total int
	if err := r.db.QueryRow(queryCtx, `
select count(*)
from public.settlement as s
where `+settlementVisibility+`
  and ($4 = '' or s."technicianId"::text = $4)
  and ($5 = '' or s."companyId"::text = $5)
  and ($6 = '' or s.status = $6)
  and ($7 = '' or s."startDate" >= $7::date)
  and ($8 = '' or s."startDate" < ($8::date + interval '1 day'))`, args...).Scan(&total); err != nil {
		return domain.SettlementPage{}, mapSettlementError(err)
	}
	rows, err := r.db.Query(queryCtx, settlementHeaderSelect+`
where `+settlementVisibility+`
  and ($4 = '' or s."technicianId"::text = $4)
  and ($5 = '' or s."companyId"::text = $5)
  and ($6 = '' or s.status = $6)
  and ($7 = '' or s."startDate" >= $7::date)
  and ($8 = '' or s."startDate" < ($8::date + interval '1 day'))
order by s."startDate" desc, s.id desc
limit $9 offset $10`, append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)...)
	if err != nil {
		return domain.SettlementPage{}, mapSettlementError(err)
	}
	defer rows.Close()
	settlements := make([]domain.Settlement, 0)
	for rows.Next() {
		settlement, err := scanSettlementHeader(rows)
		if err != nil {
			return domain.SettlementPage{}, fmt.Errorf("scan settlement: %w", err)
		}
		settlements = append(settlements, settlement)
	}
	if err := rows.Err(); err != nil {
		return domain.SettlementPage{}, fmt.Errorf("read settlements: %w", err)
	}
	return domain.SettlementPage{Data: settlements, Page: filter.Page, PageSize: filter.PageSize, Total: total}, nil
}

func (r *SettlementRepository) GetSettlement(ctx context.Context, id string, actor domain.ServiceActor) (domain.Settlement, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	return r.getSettlement(queryCtx, r.db, id, actor)
}

func (r *SettlementRepository) getSettlement(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, id string, actor domain.ServiceActor) (domain.Settlement, error) {
	settlement, err := scanSettlementHeader(q.QueryRow(ctx, settlementHeaderSelect+`
where s.id = $4::uuid
  and `+settlementVisibility, actor.Role, actor.UserID, actor.CompanyIDs, id))
	if err != nil {
		return domain.Settlement{}, mapSettlementError(err)
	}
	items, err := loadSettlementItems(ctx, q, settlement.ID)
	if err != nil {
		return domain.Settlement{}, err
	}
	parts, err := loadSettlementParts(ctx, q, settlement.ID)
	if err != nil {
		return domain.Settlement{}, err
	}
	expenses, err := loadSettlementExpenses(ctx, q, settlement.ID)
	if err != nil {
		return domain.Settlement{}, err
	}
	adjustments, err := loadSettlementAdjustments(ctx, q, settlement.ID)
	if err != nil {
		return domain.Settlement{}, err
	}
	settlement.Items, settlement.Parts, settlement.Expenses, settlement.Adjustments = items, parts, expenses, adjustments
	return settlement, nil
}

func scanSettlementHeader(row pgx.Row) (domain.Settlement, error) {
	var settlement domain.Settlement
	var companyID, companyName, label, liquidatedByID, createdByID pgtype.Text
	var liquidatedAt pgtype.Timestamptz
	var startDate, endDate time.Time
	var status string
	if err := row.Scan(
		&settlement.ID, &settlement.TechnicianID, &settlement.TechnicianName,
		&companyID, &companyName, &startDate, &endDate, &label,
		&status, &settlement.TechnicianRate, &settlement.CompanyRate,
		&settlement.ServicesCount,
		&settlement.TotalCollected, &settlement.TotalDebt, &settlement.TotalPartsCost, &settlement.TotalPartsSale,
		&settlement.TotalExpenses, &settlement.TotalInsurance, &settlement.TotalOther,
		&settlement.TotalDiscounts, &settlement.TotalAdvances, &settlement.CommissionBase,
		&settlement.TechCommission, &settlement.CompCommission, &settlement.TechBalance,
		&liquidatedAt, &liquidatedByID, &createdByID, &settlement.CreatedAt, &settlement.UpdatedAt,
	); err != nil {
		return domain.Settlement{}, err
	}
	if status == "PAID" {
		status = domain.SettlementStatusLiquidated
	}
	settlement.Status = status
	settlement.StartDate = startDate.In(settlementArtZone()).Format("2006-01-02")
	settlement.EndDate = endDate.In(settlementArtZone()).Format("2006-01-02")
	if companyID.Valid {
		value := companyID.String
		settlement.CompanyID = &value
	}
	if companyName.Valid {
		value := companyName.String
		settlement.CompanyName = &value
	}
	if label.Valid {
		value := label.String
		settlement.Label = &value
	}
	if liquidatedAt.Valid {
		value := liquidatedAt.Time
		settlement.LiquidatedAt = &value
	}
	if liquidatedByID.Valid {
		value := liquidatedByID.String
		settlement.LiquidatedByID = &value
	}
	if createdByID.Valid {
		value := createdByID.String
		settlement.CreatedByID = &value
	}
	return settlement, nil
}

func loadSettlementItems(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, settlementID string) ([]domain.SettlementItem, error) {
	rows, err := q.Query(ctx, `
select si.id::text, si."serviceId"::text, coalesce(sv."orderNumber", 0)::text, coalesce(cl.name, ''),
       si."paymentId"::text, si."serviceAmount"::text, si."collectedAmount"::text, si."debtAmount"::text,
       si."partsCostAmount"::text, si."partsSaleAmount"::text,
       coalesce((select sum(se."amountArs") from public."settlementExpense" as se
                 where se."settlementId" = si."settlementId" and se."serviceId" = si."serviceId"), 0)::text,
       si."commissionBase"::text, si."commissionRate"::text,
       si."technicianAmount"::text, si."companyAmount"::text
from public."settlementItem" as si
join public."service" as sv on sv.id = si."serviceId"
left join public.client as cl on cl.id = sv."clientId"
where si."settlementId" = $1::uuid
order by sv."orderNumber" asc, si.id asc`, settlementID)
	if err != nil {
		return nil, fmt.Errorf("query settlement items: %w", err)
	}
	defer rows.Close()
	items := make([]domain.SettlementItem, 0)
	for rows.Next() {
		var item domain.SettlementItem
		var paymentID pgtype.Text
		if err := rows.Scan(
			&item.ID, &item.ServiceID, &item.OrderNumber, &item.ClientName, &paymentID,
			&item.ServiceAmount, &item.CollectedAmount, &item.DebtAmount,
			&item.PartsCostAmount, &item.PartsSaleAmount, &item.ExpensesAmount,
			&item.CommissionBase, &item.CommissionRate, &item.TechnicianAmount, &item.CompanyAmount,
		); err != nil {
			return nil, fmt.Errorf("scan settlement item: %w", err)
		}
		if paymentID.Valid {
			value := paymentID.String
			item.PaymentID = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement items: %w", err)
	}
	return items, nil
}

func loadSettlementParts(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, settlementID string) ([]domain.SettlementPart, error) {
	rows, err := q.Query(ctx, `
select sp.id::text, sp."serviceId"::text, coalesce(sv."orderNumber", 0)::text,
       sp."companyId"::text, sp."providerName", sp.name, sp.quantity::text,
       sp."unitCost"::text, sp."totalCost"::text, sp."unitSalePrice"::text, sp."totalSalePrice"::text,
       sp."costUsd"::text, sp."exchangeRate"::text
from public."settlementPart" as sp
join public."service" as sv on sv.id = sp."serviceId"
where sp."settlementId" = $1::uuid
order by sv."orderNumber" asc, sp."createdAt" asc, sp.id asc`, settlementID)
	if err != nil {
		return nil, fmt.Errorf("query settlement parts: %w", err)
	}
	defer rows.Close()
	parts := make([]domain.SettlementPart, 0)
	for rows.Next() {
		var part domain.SettlementPart
		var companyID, providerName, costUsd, exchangeRate pgtype.Text
		if err := rows.Scan(
			&part.ID, &part.ServiceID, &part.OrderNumber, &companyID, &providerName,
			&part.Name, &part.Quantity, &part.UnitCost, &part.TotalCost,
			&part.UnitSalePrice, &part.TotalSalePrice, &costUsd, &exchangeRate,
		); err != nil {
			return nil, fmt.Errorf("scan settlement part: %w", err)
		}
		if companyID.Valid {
			value := companyID.String
			part.CompanyID = &value
		}
		if providerName.Valid {
			value := providerName.String
			part.ProviderName = &value
		}
		if costUsd.Valid {
			value := costUsd.String
			part.CostUsd = &value
		}
		if exchangeRate.Valid {
			value := exchangeRate.String
			part.ExchangeRate = &value
		}
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement parts: %w", err)
	}
	return parts, nil
}

func loadSettlementExpenses(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, settlementID string) ([]domain.SettlementExpense, error) {
	rows, err := q.Query(ctx, `
select se.id::text, se."serviceId"::text, coalesce(sv."orderNumber", 0)::text, se."expenseId"::text,
       se.concept, se.amount::text, se.currency, se."amountArs"::text
from public."settlementExpense" as se
join public."service" as sv on sv.id = se."serviceId"
where se."settlementId" = $1::uuid
order by sv."orderNumber" asc, se.id asc`, settlementID)
	if err != nil {
		return nil, fmt.Errorf("query settlement expenses: %w", err)
	}
	defer rows.Close()
	expenses := make([]domain.SettlementExpense, 0)
	for rows.Next() {
		var expense domain.SettlementExpense
		var expenseID pgtype.Text
		if err := rows.Scan(&expense.ID, &expense.ServiceID, &expense.OrderNumber, &expenseID,
			&expense.Concept, &expense.Amount, &expense.Currency, &expense.AmountArs); err != nil {
			return nil, fmt.Errorf("scan settlement expense: %w", err)
		}
		if expenseID.Valid {
			value := expenseID.String
			expense.ExpenseID = &value
		}
		expenses = append(expenses, expense)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement expenses: %w", err)
	}
	return expenses, nil
}

func loadSettlementAdjustments(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, settlementID string) ([]domain.SettlementAdjustment, error) {
	rows, err := q.Query(ctx, `
select id::text, kind, concept, amount::text
from public."settlementAdjustment"
where "settlementId" = $1::uuid
order by kind asc, id asc`, settlementID)
	if err != nil {
		return nil, fmt.Errorf("query settlement adjustments: %w", err)
	}
	defer rows.Close()
	adjustments := make([]domain.SettlementAdjustment, 0)
	for rows.Next() {
		var adjustment domain.SettlementAdjustment
		if err := rows.Scan(&adjustment.ID, &adjustment.Kind, &adjustment.Concept, &adjustment.Amount); err != nil {
			return nil, fmt.Errorf("scan settlement adjustment: %w", err)
		}
		adjustments = append(adjustments, adjustment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement adjustments: %w", err)
	}
	return adjustments, nil
}

type settlementPayment struct {
	PaymentID     string
	ServiceID     string
	OrderNumber   string
	ClientName    string
	ServiceAmount string
	Collected     string
	Debt          string
	CompanyID     string
}

type settlementPartRow struct {
	ServiceID      string
	CompanyID      *string
	ProviderName   *string
	Name           string
	Quantity       string
	UnitCost       string
	TotalCost      string
	UnitSalePrice  string
	TotalSalePrice string
	CostUsd        *string
	ExchangeRate   *string
}

type settlementExpenseRow struct {
	ID        string
	ServiceID string
	Concept   string
	Amount    string
	Currency  string
	AmountArs string
}

type settlementRawItem struct {
	PaymentID        string
	ServiceID        string
	OrderNumber      string
	ClientName       string
	ServiceAmount    string
	Collected        string
	Debt             string
	PartsCost        string
	PartsSale        string
	ExpensesArs      string
	RawBase          string
	CommissionBase   string
	TechnicianAmount string
	CompanyAmount    string
}

type settlementTotals struct {
	TotalCollected string
	TotalDebt      string
	TotalPartsCost string
	TotalPartsSale string
	TotalExpenses  string
	TotalInsurance string
	TotalOther     string
	TotalDiscounts string
	TotalAdvances  string
	CommissionBase string
	TechCommission string
	CompCommission string
	TechBalance    string
	TechRate       string
}

func (r *SettlementRepository) GenerateSettlement(ctx context.Context, draft domain.SettlementDraft, actor domain.ServiceActor) (domain.Settlement, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Settlement{}, fmt.Errorf("begin settlement generation: %w", err)
	}
	defer rollback(tx)

	if _, err := tx.Exec(queryCtx, `select pg_advisory_xact_lock(hashtext($1))`, draft.TechnicianID); err != nil {
		return domain.Settlement{}, fmt.Errorf("lock settlement technician: %w", err)
	}
	var technicianName, technicianRole string
	var technicianActive bool
	if err := tx.QueryRow(queryCtx, `
select name, role, "isActive" from public."user" where id = $1::uuid`, draft.TechnicianID).Scan(
		&technicianName, &technicianRole, &technicianActive); err != nil {
		return domain.Settlement{}, mapSettlementError(err)
	}
	if technicianRole != "TECHNICIAN" || !technicianActive {
		return domain.Settlement{}, outbound.ErrRuleViolation
	}
	if draft.CompanyID != "" && !containsID(actor.CompanyIDs, draft.CompanyID) {
		return domain.Settlement{}, outbound.ErrRuleViolation
	}

	var existingID, existingStatus string
	var existingStart, existingEnd time.Time
	overlapErr := tx.QueryRow(queryCtx, `
select id::text, status, "startDate", "endDate"
from public.settlement
where "technicianId" = $1::uuid
  and "startDate" <= $3
  and "endDate" >= $2
order by "startDate" asc
limit 1
for update`, draft.TechnicianID, draft.StartAt, draft.EndAt).Scan(
		&existingID, &existingStatus, &existingStart, &existingEnd)
	if overlapErr == nil {
		samePeriod := existingStart.Equal(draft.StartAt) && existingEnd.Equal(draft.EndAt)
		if existingStatus != "PENDING" || !samePeriod {
			return domain.Settlement{}, outbound.ErrConflict
		}
		if _, err := tx.Exec(queryCtx, `update public.payment set "settlementId" = null, "updatedAt" = now() where "settlementId" = $1::uuid`, existingID); err != nil {
			return domain.Settlement{}, fmt.Errorf("detach payments from previous settlement: %w", err)
		}
		if _, err := tx.Exec(queryCtx, `delete from public.settlement where id = $1::uuid`, existingID); err != nil {
			return domain.Settlement{}, fmt.Errorf("remove previous settlement draft: %w", err)
		}
	} else if !errors.Is(overlapErr, pgx.ErrNoRows) {
		return domain.Settlement{}, mapSettlementError(overlapErr)
	}

	payments, err := loadSettlementPayments(queryCtx, tx, draft, actor.CompanyIDs)
	if err != nil {
		return domain.Settlement{}, err
	}
	if len(payments) == 0 {
		return domain.Settlement{}, outbound.ErrRuleViolation
	}
	serviceIDs := make([]string, 0, len(payments))
	for _, payment := range payments {
		serviceIDs = append(serviceIDs, payment.ServiceID)
	}
	parts, err := loadSettlementPartRows(queryCtx, tx, serviceIDs)
	if err != nil {
		return domain.Settlement{}, err
	}
	expenses, err := loadSettlementExpenseRows(queryCtx, tx, serviceIDs)
	if err != nil {
		return domain.Settlement{}, err
	}
	fxRate, hasFX, err := settlementExchangeRate(queryCtx, tx)
	if err != nil {
		return domain.Settlement{}, err
	}
	rawItems, convertedExpenses, err := buildSettlementRawItems(payments, parts, expenses, fxRate, hasFX)
	if err != nil {
		return domain.Settlement{}, err
	}
	totals, err := computeSettlementTotals(rawItems, draft.Insurance, draft.Adjustments, draft.CompanyRate)
	if err != nil {
		return domain.Settlement{}, err
	}
	label := strings.TrimSpace(draft.Label)
	if label == "" {
		label = "Rendición " + technicianName + " — " + draft.StartDate + " al " + draft.EndDate
	}
	settlementID, err := insertSettlement(queryCtx, tx, draft, label, totals, len(payments))
	if err != nil {
		return domain.Settlement{}, err
	}
	if err := insertSettlementItems(queryCtx, tx, settlementID, rawItems, totals.TechRate); err != nil {
		return domain.Settlement{}, err
	}
	if err := insertSettlementParts(queryCtx, tx, settlementID, parts); err != nil {
		return domain.Settlement{}, err
	}
	if err := insertSettlementExpenses(queryCtx, tx, settlementID, convertedExpenses); err != nil {
		return domain.Settlement{}, err
	}
	if err := insertSettlementAdjustments(queryCtx, tx, settlementID, draft.Insurance, draft.Adjustments); err != nil {
		return domain.Settlement{}, err
	}
	paymentIDs := make([]string, 0, len(payments))
	for _, payment := range payments {
		paymentIDs = append(paymentIDs, payment.PaymentID)
	}
	if _, err := tx.Exec(queryCtx, `
update public.payment set "settlementId" = $1::uuid, "updatedAt" = now()
where id = any($2::uuid[])`, settlementID, paymentIDs); err != nil {
		return domain.Settlement{}, fmt.Errorf("attach payments to settlement: %w", err)
	}
	if err := writeSettlementActivity(queryCtx, tx, settlementID, rawItems, actor.UserID, "SETTLEMENT_GENERATED", "Rendición generada"); err != nil {
		return domain.Settlement{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Settlement{}, fmt.Errorf("commit settlement generation: %w", err)
	}
	return r.GetSettlement(queryCtx, settlementID, actor)
}

func loadSettlementPayments(ctx context.Context, tx pgx.Tx, draft domain.SettlementDraft, companyIDs []string) ([]settlementPayment, error) {
	rows, err := tx.Query(ctx, `
select p.id::text, p."serviceId"::text, coalesce(s."orderNumber", 0)::text, coalesce(cl.name, ''),
       coalesce(s."finalAmount", 0)::text, p."amountPaid"::text, p."debtAmount"::text, s."companyId"::text
from public.payment as p
join public."service" as s on s.id = p."serviceId"
left join public.client as cl on cl.id = s."clientId"
where p."technicianId" = $1::uuid
  and p."createdAt" >= $2
  and p."createdAt" <= $3
  and p."settlementId" is null
  and s."companyId"::text = any($5::text[])
  and ($4 = '' or s."companyId"::text = $4)
order by p."createdAt" asc, p.id asc`, draft.TechnicianID, draft.StartAt, draft.EndAt, draft.CompanyID, companyIDs)
	if err != nil {
		return nil, fmt.Errorf("query settlement payments: %w", err)
	}
	defer rows.Close()
	payments := make([]settlementPayment, 0)
	for rows.Next() {
		var payment settlementPayment
		if err := rows.Scan(&payment.PaymentID, &payment.ServiceID, &payment.OrderNumber, &payment.ClientName,
			&payment.ServiceAmount, &payment.Collected, &payment.Debt, &payment.CompanyID); err != nil {
			return nil, fmt.Errorf("scan settlement payment: %w", err)
		}
		payments = append(payments, payment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement payments: %w", err)
	}
	return payments, nil
}

func loadSettlementPartRows(ctx context.Context, tx pgx.Tx, serviceIDs []string) ([]settlementPartRow, error) {
	rows, err := tx.Query(ctx, `
select p."serviceId"::text, p."companyId"::text, p."providerName", p.name, p.quantity::text,
       p."unitCost"::text, p."totalCost"::text, p."unitSalePrice"::text, p."totalSalePrice"::text,
       p."costUsd"::text, p."exchangeRate"::text
from public."servicePart" as p
where p."serviceId" = any($1::uuid[])
order by p."serviceId" asc, p."createdAt" asc, p.id asc`, serviceIDs)
	if err != nil {
		return nil, fmt.Errorf("query settlement parts: %w", err)
	}
	defer rows.Close()
	parts := make([]settlementPartRow, 0)
	for rows.Next() {
		var part settlementPartRow
		var companyID, providerName, costUsd, exchangeRate pgtype.Text
		if err := rows.Scan(&part.ServiceID, &companyID, &providerName, &part.Name, &part.Quantity,
			&part.UnitCost, &part.TotalCost, &part.UnitSalePrice, &part.TotalSalePrice, &costUsd, &exchangeRate); err != nil {
			return nil, fmt.Errorf("scan settlement part: %w", err)
		}
		part.CompanyID = textPointer(companyID)
		part.ProviderName = textPointer(providerName)
		part.CostUsd = textPointer(costUsd)
		part.ExchangeRate = textPointer(exchangeRate)
		parts = append(parts, part)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement parts: %w", err)
	}
	return parts, nil
}

func loadSettlementExpenseRows(ctx context.Context, tx pgx.Tx, serviceIDs []string) ([]settlementExpenseRow, error) {
	rows, err := tx.Query(ctx, `
select e.id::text, e."serviceId"::text, e.concept, e.amount::text, e.currency
from public."serviceExpense" as e
where e."serviceId" = any($1::uuid[])
order by e."serviceId" asc, e."createdAt" asc, e.id asc`, serviceIDs)
	if err != nil {
		return nil, fmt.Errorf("query settlement expenses: %w", err)
	}
	defer rows.Close()
	expenses := make([]settlementExpenseRow, 0)
	for rows.Next() {
		var expense settlementExpenseRow
		if err := rows.Scan(&expense.ID, &expense.ServiceID, &expense.Concept, &expense.Amount, &expense.Currency); err != nil {
			return nil, fmt.Errorf("scan settlement expense: %w", err)
		}
		expenses = append(expenses, expense)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement expenses: %w", err)
	}
	return expenses, nil
}

func settlementExchangeRate(ctx context.Context, tx pgx.Tx) (string, bool, error) {
	var rate string
	if err := tx.QueryRow(ctx, `
select rate::text from public."exchangeRate"
order by "rateDate" desc
limit 1`).Scan(&rate); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read settlement exchange rate: %w", err)
	}
	return rate, true, nil
}

func buildSettlementRawItems(payments []settlementPayment, parts []settlementPartRow, expenses []settlementExpenseRow, fxRate string, hasFX bool) ([]settlementRawItem, []settlementExpenseRow, error) {
	partsCost := map[string]string{}
	partsSale := map[string]string{}
	for _, part := range parts {
		cost, err := money.Add(zeroIfEmpty(partsCost[part.ServiceID]), part.TotalCost)
		if err != nil {
			return nil, nil, err
		}
		sale, err := money.Add(zeroIfEmpty(partsSale[part.ServiceID]), part.TotalSalePrice)
		if err != nil {
			return nil, nil, err
		}
		partsCost[part.ServiceID], partsSale[part.ServiceID] = cost, sale
	}
	expensesArs := map[string]string{}
	converted := make([]settlementExpenseRow, 0, len(expenses))
	for _, expense := range expenses {
		amountArs := expense.Amount
		if expense.Currency == "USD" {
			if !hasFX {
				return nil, nil, outbound.ErrRuleViolation
			}
			value, err := money.Mul(expense.Amount, fxRate)
			if err != nil {
				return nil, nil, err
			}
			amountArs, err = money.Round(value, 2)
			if err != nil {
				return nil, nil, err
			}
		}
		expense.AmountArs = amountArs
		converted = append(converted, expense)
		total, err := money.Add(zeroIfEmpty(expensesArs[expense.ServiceID]), amountArs)
		if err != nil {
			return nil, nil, err
		}
		expensesArs[expense.ServiceID] = total
	}
	items := make([]settlementRawItem, 0, len(payments))
	for _, payment := range payments {
		cost := zeroIfEmpty(partsCost[payment.ServiceID])
		sale := zeroIfEmpty(partsSale[payment.ServiceID])
		serviceExpenses := zeroIfEmpty(expensesArs[payment.ServiceID])
		base, err := money.Sub(payment.Collected, cost)
		if err != nil {
			return nil, nil, err
		}
		base, err = money.Sub(base, serviceExpenses)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, settlementRawItem{
			PaymentID: payment.PaymentID, ServiceID: payment.ServiceID,
			OrderNumber: payment.OrderNumber, ClientName: payment.ClientName,
			ServiceAmount: payment.ServiceAmount, Collected: payment.Collected, Debt: payment.Debt,
			PartsCost: cost, PartsSale: sale, ExpensesArs: serviceExpenses, RawBase: base,
		})
	}
	return items, converted, nil
}

func computeSettlementTotals(items []settlementRawItem, insurance, adjustments []domain.SettlementAdjustmentDraft, companyRate string) (settlementTotals, error) {
	totalCollected, totalDebt, totalPartsCost, totalPartsSale, totalExpenses := "0", "0", "0", "0", "0"
	rawBaseTotal := "0"
	for _, item := range items {
		var err error
		if totalCollected, err = money.Add(totalCollected, item.Collected); err != nil {
			return settlementTotals{}, err
		}
		if totalDebt, err = money.Add(totalDebt, item.Debt); err != nil {
			return settlementTotals{}, err
		}
		if totalPartsCost, err = money.Add(totalPartsCost, item.PartsCost); err != nil {
			return settlementTotals{}, err
		}
		if totalPartsSale, err = money.Add(totalPartsSale, item.PartsSale); err != nil {
			return settlementTotals{}, err
		}
		if totalExpenses, err = money.Add(totalExpenses, item.ExpensesArs); err != nil {
			return settlementTotals{}, err
		}
		if rawBaseTotal, err = money.Add(rawBaseTotal, item.RawBase); err != nil {
			return settlementTotals{}, err
		}
	}
	totalInsurance, totalOther, totalDiscounts, totalAdvances := "0", "0", "0", "0"
	for _, adjustment := range insurance {
		value, err := money.Add(totalInsurance, adjustment.Amount)
		if err != nil {
			return settlementTotals{}, err
		}
		totalInsurance = value
	}
	for _, adjustment := range adjustments {
		var target *string
		switch adjustment.Kind {
		case domain.AdjustmentOther:
			target = &totalOther
		case domain.AdjustmentDiscount:
			target = &totalDiscounts
		case domain.AdjustmentAdvance:
			target = &totalAdvances
		default:
			return settlementTotals{}, outbound.ErrRuleViolation
		}
		value, err := money.Add(*target, adjustment.Amount)
		if err != nil {
			return settlementTotals{}, err
		}
		*target = value
	}
	base, err := money.Sub(totalCollected, totalPartsCost)
	if err != nil {
		return settlementTotals{}, err
	}
	for _, deduction := range []string{totalExpenses, totalInsurance, totalOther} {
		if base, err = money.Sub(base, deduction); err != nil {
			return settlementTotals{}, err
		}
	}
	techRate, err := money.Sub("1", companyRate)
	if err != nil {
		return settlementTotals{}, err
	}
	companyCommissionValue, err := money.Mul(base, companyRate)
	if err != nil {
		return settlementTotals{}, err
	}
	companyCommission, err := money.Round(companyCommissionValue, 2)
	if err != nil {
		return settlementTotals{}, err
	}
	techCommission, err := money.Sub(base, companyCommission)
	if err != nil {
		return settlementTotals{}, err
	}
	techBalance, err := money.Sub(techCommission, totalDiscounts)
	if err != nil {
		return settlementTotals{}, err
	}
	if techBalance, err = money.Sub(techBalance, totalAdvances); err != nil {
		return settlementTotals{}, err
	}
	deductions, err := money.Add(totalInsurance, totalOther)
	if err != nil {
		return settlementTotals{}, err
	}
	allocateSettlementDeductions(items, deductions, rawBaseTotal)
	for index := range items {
		techAmountValue, err := money.Mul(items[index].CommissionBase, techRate)
		if err != nil {
			return settlementTotals{}, err
		}
		techAmount, err := money.Round(techAmountValue, 2)
		if err != nil {
			return settlementTotals{}, err
		}
		items[index].TechnicianAmount = techAmount
		items[index].CompanyAmount, err = money.Sub(items[index].CommissionBase, techAmount)
		if err != nil {
			return settlementTotals{}, err
		}
	}
	return settlementTotals{
		TotalCollected: totalCollected, TotalDebt: totalDebt, TotalPartsCost: totalPartsCost,
		TotalPartsSale: totalPartsSale, TotalExpenses: totalExpenses,
		TotalInsurance: totalInsurance, TotalOther: totalOther,
		TotalDiscounts: totalDiscounts, TotalAdvances: totalAdvances,
		CommissionBase: base, TechCommission: techCommission, CompCommission: companyCommission,
		TechBalance: techBalance, TechRate: techRate,
	}, nil
}

func allocateSettlementDeductions(items []settlementRawItem, deductions, rawBaseTotal string) {
	if len(items) == 0 {
		return
	}
	if deductions == "0" {
		for index := range items {
			items[index].CommissionBase = items[index].RawBase
		}
		return
	}
	allocated := "0"
	last := len(items) - 1
	for index := 0; index < last; index++ {
		value := "0"
		if rawBaseTotal != "0" {
			rounded, err := money.MulDivRound(deductions, items[index].RawBase, rawBaseTotal, 2)
			if err == nil {
				value = rounded
			}
		}
		items[index].CommissionBase, _ = money.Sub(items[index].RawBase, value)
		allocated, _ = money.Add(allocated, value)
	}
	remaining, err := money.Sub(deductions, allocated)
	if err != nil {
		remaining = deductions
	}
	items[last].CommissionBase, _ = money.Sub(items[last].RawBase, remaining)
}

func insertSettlement(ctx context.Context, tx pgx.Tx, draft domain.SettlementDraft, label string, totals settlementTotals, servicesCount int) (string, error) {
	var companyID any
	if draft.CompanyID != "" {
		companyID = draft.CompanyID
	}
	var settlementID string
	if err := tx.QueryRow(ctx, `
insert into public.settlement
  ("technicianId", "companyId", "startDate", "endDate", label, "commissionRate", "servicesCount",
   "totalCollected", "totalDebt", "totalPartsCost", "totalPartsSale", "commissionBase",
   "techCommission", "compCommission", status, "companyRate", "totalExpenses", "totalInsurance",
   "totalOtherAdjustments", "totalDiscounts", "totalAdvances", "techBalance", "createdById")
values ($1::uuid, $2::uuid, $3, $4, $5, $6::numeric, $7,
        $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric,
        $13::numeric, $14::numeric, 'PENDING', $15::numeric, $16::numeric, $17::numeric,
        $18::numeric, $19::numeric, $20::numeric, $21::numeric, $22::uuid)
returning id::text`,
		draft.TechnicianID, companyID, draft.StartAt, draft.EndAt, label, totals.TechRate, servicesCount,
		totals.TotalCollected, totals.TotalDebt, totals.TotalPartsCost, totals.TotalPartsSale, totals.CommissionBase,
		totals.TechCommission, totals.CompCommission, draft.CompanyRate, totals.TotalExpenses, totals.TotalInsurance,
		totals.TotalOther, totals.TotalDiscounts, totals.TotalAdvances, totals.TechBalance, draft.CreatedByID).Scan(&settlementID); err != nil {
		return "", mapSettlementError(err)
	}
	return settlementID, nil
}

func insertSettlementItems(ctx context.Context, tx pgx.Tx, settlementID string, items []settlementRawItem, techRate string) error {
	for _, item := range items {
		if _, err := tx.Exec(ctx, `
insert into public."settlementItem"
  ("settlementId", "serviceId", "paymentId", "serviceAmount", "collectedAmount", "debtAmount",
   "partsCostAmount", "partsSaleAmount", "commissionBase", "commissionRate", "technicianAmount", "companyAmount")
values ($1::uuid, $2::uuid, $3::uuid, $4::numeric, $5::numeric, $6::numeric,
        $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric)`,
			settlementID, item.ServiceID, item.PaymentID, item.ServiceAmount, item.Collected, item.Debt,
			item.PartsCost, item.PartsSale, item.CommissionBase, techRate, item.TechnicianAmount, item.CompanyAmount); err != nil {
			return fmt.Errorf("insert settlement item: %w", err)
		}
	}
	return nil
}

func insertSettlementParts(ctx context.Context, tx pgx.Tx, settlementID string, parts []settlementPartRow) error {
	for _, part := range parts {
		if _, err := tx.Exec(ctx, `
insert into public."settlementPart"
  ("settlementId", "serviceId", "companyId", "providerName", name, quantity,
   "unitCost", "totalCost", "unitSalePrice", "totalSalePrice", "costUsd", "exchangeRate")
values ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::numeric,
        $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric)`,
			settlementID, part.ServiceID, part.CompanyID, part.ProviderName, part.Name, part.Quantity,
			part.UnitCost, part.TotalCost, part.UnitSalePrice, part.TotalSalePrice, part.CostUsd, part.ExchangeRate); err != nil {
			return fmt.Errorf("insert settlement part: %w", err)
		}
	}
	return nil
}

func insertSettlementExpenses(ctx context.Context, tx pgx.Tx, settlementID string, expenses []settlementExpenseRow) error {
	for _, expense := range expenses {
		if _, err := tx.Exec(ctx, `
insert into public."settlementExpense"
  ("settlementId", "serviceId", "expenseId", concept, amount, currency, "amountArs")
values ($1::uuid, $2::uuid, $3::uuid, $4, $5::numeric, $6, $7::numeric)`,
			settlementID, expense.ServiceID, expense.ID, expense.Concept, expense.Amount, expense.Currency, expense.AmountArs); err != nil {
			return fmt.Errorf("insert settlement expense: %w", err)
		}
	}
	return nil
}

func insertSettlementAdjustments(ctx context.Context, tx pgx.Tx, settlementID string, insurance, adjustments []domain.SettlementAdjustmentDraft) error {
	for _, group := range [][]domain.SettlementAdjustmentDraft{insurance, adjustments} {
		for _, adjustment := range group {
			if _, err := tx.Exec(ctx, `
insert into public."settlementAdjustment" ("settlementId", kind, concept, amount)
values ($1::uuid, $2, $3, $4::numeric)`,
				settlementID, adjustment.Kind, adjustment.Concept, adjustment.Amount); err != nil {
				return fmt.Errorf("insert settlement adjustment: %w", err)
			}
		}
	}
	return nil
}

func writeSettlementActivity(ctx context.Context, tx pgx.Tx, settlementID string, items []settlementRawItem, userID, action, description string) error {
	metadata, err := json.Marshal(map[string]string{"settlementId": settlementID})
	if err != nil {
		return fmt.Errorf("encode settlement audit: %w", err)
	}
	seen := map[string]struct{}{}
	for _, item := range items {
		if _, exists := seen[item.ServiceID]; exists {
			continue
		}
		seen[item.ServiceID] = struct{}{}
		if err := insertActivity(ctx, tx, domain.ActivityRecord{
			ServiceID: item.ServiceID, UserID: userID, Action: action, Description: description, Metadata: metadata,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *SettlementRepository) UpdateSettlement(ctx context.Context, id string, patch domain.SettlementPatch, actor domain.ServiceActor) (domain.Settlement, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Settlement{}, fmt.Errorf("begin settlement update: %w", err)
	}
	defer rollback(tx)

	if _, err := tx.Exec(queryCtx, `select 1 from public.settlement where id = $1::uuid for update`, id); err != nil {
		return domain.Settlement{}, mapSettlementError(err)
	}
	current, err := r.getSettlement(queryCtx, tx, id, actor)
	if err != nil {
		return domain.Settlement{}, err
	}
	if current.Status != domain.SettlementStatusPending {
		return domain.Settlement{}, outbound.ErrConflict
	}
	label := ""
	if current.Label != nil {
		label = *current.Label
	}
	if patch.Label.Set {
		label = *patch.Label.Value
	}
	companyRate := current.CompanyRate
	if patch.CompanyRate.Set {
		companyRate = *patch.CompanyRate.Value
	}
	insurance, adjustments := splitSettlementAdjustments(current.Adjustments)
	if patch.Insurance != nil {
		insurance = *patch.Insurance
	}
	if patch.Adjustments != nil {
		adjustments = *patch.Adjustments
	}
	rawItems, err := loadSettlementRawItemsForUpdate(queryCtx, tx, id)
	if err != nil {
		return domain.Settlement{}, err
	}
	totals, err := computeSettlementTotals(rawItems, insurance, adjustments, companyRate)
	if err != nil {
		return domain.Settlement{}, err
	}
	if _, err := tx.Exec(queryCtx, `
update public.settlement
set label = $2, "companyRate" = $3::numeric, "commissionRate" = $4::numeric,
    "totalExpenses" = $5::numeric, "totalInsurance" = $6::numeric, "totalOtherAdjustments" = $7::numeric,
    "totalDiscounts" = $8::numeric, "totalAdvances" = $9::numeric, "commissionBase" = $10::numeric,
    "techCommission" = $11::numeric, "compCommission" = $12::numeric, "techBalance" = $13::numeric,
    "updatedAt" = now()
where id = $1::uuid`,
		id, label, companyRate, totals.TechRate, totals.TotalExpenses, totals.TotalInsurance, totals.TotalOther,
		totals.TotalDiscounts, totals.TotalAdvances, totals.CommissionBase, totals.TechCommission,
		totals.CompCommission, totals.TechBalance); err != nil {
		return domain.Settlement{}, mapSettlementError(err)
	}
	if patch.Insurance != nil || patch.Adjustments != nil {
		if _, err := tx.Exec(queryCtx, `delete from public."settlementAdjustment" where "settlementId" = $1::uuid`, id); err != nil {
			return domain.Settlement{}, fmt.Errorf("replace settlement adjustments: %w", err)
		}
		if err := insertSettlementAdjustments(queryCtx, tx, id, insurance, adjustments); err != nil {
			return domain.Settlement{}, err
		}
	}
	for index, item := range rawItems {
		if _, err := tx.Exec(queryCtx, `
update public."settlementItem"
set "commissionBase" = $2::numeric, "commissionRate" = $3::numeric,
    "technicianAmount" = $4::numeric, "companyAmount" = $5::numeric
where "settlementId" = $1::uuid and "serviceId" = $6::uuid`,
			id, item.CommissionBase, totals.TechRate, item.TechnicianAmount, item.CompanyAmount, item.ServiceID); err != nil {
			return domain.Settlement{}, fmt.Errorf("update settlement item %d: %w", index, err)
		}
	}
	if err := writeSettlementActivity(queryCtx, tx, id, rawItems, actor.UserID, "SETTLEMENT_UPDATED", "Rendición corregida"); err != nil {
		return domain.Settlement{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Settlement{}, fmt.Errorf("commit settlement update: %w", err)
	}
	return r.GetSettlement(queryCtx, id, actor)
}

func loadSettlementRawItemsForUpdate(ctx context.Context, tx pgx.Tx, settlementID string) ([]settlementRawItem, error) {
	rows, err := tx.Query(ctx, `
select si."paymentId"::text, si."serviceId"::text, coalesce(sv."orderNumber", 0)::text, coalesce(cl.name, ''),
       si."serviceAmount"::text, si."collectedAmount"::text, si."debtAmount"::text,
       si."partsCostAmount"::text, si."partsSaleAmount"::text,
       coalesce((select sum(se."amountArs") from public."settlementExpense" as se
                 where se."settlementId" = si."settlementId" and se."serviceId" = si."serviceId"), 0)::text
from public."settlementItem" as si
join public."service" as sv on sv.id = si."serviceId"
left join public.client as cl on cl.id = sv."clientId"
where si."settlementId" = $1::uuid
order by sv."orderNumber" asc, si.id asc`, settlementID)
	if err != nil {
		return nil, fmt.Errorf("query settlement draft items: %w", err)
	}
	defer rows.Close()
	items := make([]settlementRawItem, 0)
	for rows.Next() {
		var item settlementRawItem
		var paymentID pgtype.Text
		if err := rows.Scan(&paymentID, &item.ServiceID, &item.OrderNumber, &item.ClientName,
			&item.ServiceAmount, &item.Collected, &item.Debt, &item.PartsCost, &item.PartsSale, &item.ExpensesArs); err != nil {
			return nil, fmt.Errorf("scan settlement draft item: %w", err)
		}
		if paymentID.Valid {
			item.PaymentID = paymentID.String
		}
		base, err := money.Sub(item.Collected, item.PartsCost)
		if err != nil {
			return nil, err
		}
		base, err = money.Sub(base, item.ExpensesArs)
		if err != nil {
			return nil, err
		}
		item.RawBase = base
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settlement draft items: %w", err)
	}
	return items, nil
}

func splitSettlementAdjustments(values []domain.SettlementAdjustment) ([]domain.SettlementAdjustmentDraft, []domain.SettlementAdjustmentDraft) {
	insurance := make([]domain.SettlementAdjustmentDraft, 0)
	adjustments := make([]domain.SettlementAdjustmentDraft, 0)
	for _, value := range values {
		draft := domain.SettlementAdjustmentDraft{Kind: value.Kind, Concept: value.Concept, Amount: value.Amount}
		if value.Kind == domain.AdjustmentInsurance {
			insurance = append(insurance, draft)
		} else {
			adjustments = append(adjustments, draft)
		}
	}
	return insurance, adjustments
}

func (r *SettlementRepository) LiquidateSettlement(ctx context.Context, id string, actor domain.ServiceActor) (domain.Settlement, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()
	tx, err := r.db.Begin(queryCtx)
	if err != nil {
		return domain.Settlement{}, fmt.Errorf("begin settlement liquidation: %w", err)
	}
	defer rollback(tx)

	if _, err := tx.Exec(queryCtx, `select 1 from public.settlement where id = $1::uuid for update`, id); err != nil {
		return domain.Settlement{}, mapSettlementError(err)
	}
	current, err := r.getSettlement(queryCtx, tx, id, actor)
	if err != nil {
		return domain.Settlement{}, err
	}
	if current.Status != domain.SettlementStatusPending {
		return domain.Settlement{}, outbound.ErrConflict
	}
	if _, err := tx.Exec(queryCtx, `
update public.settlement
set status = 'PAID', "liquidatedById" = $2::uuid, "liquidatedAt" = now(), "updatedAt" = now()
where id = $1::uuid`, id, actor.UserID); err != nil {
		return domain.Settlement{}, mapSettlementError(err)
	}
	items := make([]settlementRawItem, 0, len(current.Items))
	for _, item := range current.Items {
		items = append(items, settlementRawItem{ServiceID: item.ServiceID})
	}
	if err := writeSettlementActivity(queryCtx, tx, id, items, actor.UserID, "SETTLEMENT_LIQUIDATED", "Rendición liquidada"); err != nil {
		return domain.Settlement{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return domain.Settlement{}, fmt.Errorf("commit settlement liquidation: %w", err)
	}
	return r.GetSettlement(queryCtx, id, actor)
}

func mapSettlementError(err error) error {
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
		case "42703", "42P01":
			return outbound.ErrRuleViolation
		}
	}
	return err
}

func settlementArtZone() *time.Location {
	return time.FixedZone("ART", -3*60*60)
}

func zeroIfEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "0"
	}
	return value
}

func (r *SettlementRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if r.queryTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, r.queryTimeout)
}

var _ outbound.SettlementRepository = (*SettlementRepository)(nil)
