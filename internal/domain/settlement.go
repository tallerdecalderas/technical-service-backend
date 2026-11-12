package domain

import "time"

const (
	SettlementStatusPending    = "PENDING"
	SettlementStatusLiquidated = "LIQUIDATED"

	AdjustmentInsurance = "INSURANCE"
	AdjustmentOther     = "OTHER"
	AdjustmentDiscount  = "DISCOUNT"
	AdjustmentAdvance   = "ADVANCE"
)

type Settlement struct {
	ID             string     `json:"id"`
	TechnicianID   string     `json:"technicianId"`
	TechnicianName string     `json:"technicianName"`
	CompanyID      *string    `json:"companyId,omitempty"`
	CompanyName    *string    `json:"companyName,omitempty"`
	StartDate      string     `json:"startDate"`
	EndDate        string     `json:"endDate"`
	Label          *string    `json:"label,omitempty"`
	Status         string     `json:"status"`
	CompanyRate    string     `json:"companyRate"`
	TechnicianRate string     `json:"technicianRate"`
	ServicesCount  int        `json:"servicesCount"`
	TotalCollected string     `json:"totalCollected"`
	TotalDebt      string     `json:"totalDebt"`
	TotalPartsCost string     `json:"totalPartsCost"`
	TotalPartsSale string     `json:"totalPartsSale"`
	TotalExpenses  string     `json:"totalExpenses"`
	TotalInsurance string     `json:"totalInsurance"`
	TotalOther     string     `json:"totalOtherAdjustments"`
	TotalDiscounts string     `json:"totalDiscounts"`
	TotalAdvances  string     `json:"totalAdvances"`
	CommissionBase string     `json:"commissionBase"`
	TechCommission string     `json:"techCommission"`
	CompCommission string     `json:"compCommission"`
	TechBalance    string     `json:"techBalance"`
	LiquidatedAt   *time.Time `json:"liquidatedAt,omitempty"`
	LiquidatedByID *string    `json:"liquidatedById,omitempty"`
	CreatedByID    *string    `json:"createdById,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`

	Items       []SettlementItem       `json:"items"`
	Parts       []SettlementPart       `json:"parts"`
	Expenses    []SettlementExpense    `json:"expenses"`
	Adjustments []SettlementAdjustment `json:"adjustments"`
}

type SettlementItem struct {
	ID               string  `json:"id"`
	ServiceID        string  `json:"serviceId"`
	OrderNumber      string  `json:"orderNumber"`
	ClientName       string  `json:"clientName"`
	PaymentID        *string `json:"paymentId,omitempty"`
	ServiceAmount    string  `json:"serviceAmount"`
	CollectedAmount  string  `json:"collectedAmount"`
	DebtAmount       string  `json:"debtAmount"`
	PartsCostAmount  string  `json:"partsCostAmount"`
	PartsSaleAmount  string  `json:"partsSaleAmount"`
	ExpensesAmount   string  `json:"expensesAmount"`
	CommissionBase   string  `json:"commissionBase"`
	CommissionRate   string  `json:"commissionRate"`
	TechnicianAmount string  `json:"technicianAmount"`
	CompanyAmount    string  `json:"companyAmount"`
}

type SettlementPart struct {
	ID             string  `json:"id"`
	ServiceID      string  `json:"serviceId"`
	OrderNumber    string  `json:"orderNumber"`
	CompanyID      *string `json:"companyId,omitempty"`
	ProviderName   *string `json:"providerName,omitempty"`
	Name           string  `json:"name"`
	Quantity       string  `json:"quantity"`
	UnitCost       string  `json:"unitCost"`
	TotalCost      string  `json:"totalCost"`
	UnitSalePrice  string  `json:"unitSalePrice"`
	TotalSalePrice string  `json:"totalSalePrice"`
	CostUsd        *string `json:"costUsd,omitempty"`
	ExchangeRate   *string `json:"exchangeRate,omitempty"`
}

type SettlementExpense struct {
	ID          string  `json:"id"`
	ServiceID   string  `json:"serviceId"`
	OrderNumber string  `json:"orderNumber"`
	ExpenseID   *string `json:"expenseId,omitempty"`
	Concept     string  `json:"concept"`
	Amount      string  `json:"amount"`
	Currency    string  `json:"currency"`
	AmountArs   string  `json:"amountArs"`
}

type SettlementAdjustment struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Concept string `json:"concept"`
	Amount  string `json:"amount"`
}

type SettlementFilter struct {
	Actor        ServiceActor
	TechnicianID string
	CompanyID    string
	Status       string
	DateFrom     string
	DateTo       string
	Page         int
	PageSize     int
}

type SettlementPage struct {
	Data     []Settlement
	Page     int
	PageSize int
	Total    int
}

type SettlementAdjustmentDraft struct {
	Kind    string
	Concept string
	Amount  string
}

type SettlementDraft struct {
	TechnicianID string
	CompanyID    string
	StartDate    string
	EndDate      string
	StartAt      time.Time
	EndAt        time.Time
	Label        string
	CompanyRate  string
	Insurance    []SettlementAdjustmentDraft
	Adjustments  []SettlementAdjustmentDraft
	CreatedByID  string
}

type SettlementPatch struct {
	Label       Field[string]
	CompanyRate Field[string]
	Insurance   *[]SettlementAdjustmentDraft
	Adjustments *[]SettlementAdjustmentDraft
}
