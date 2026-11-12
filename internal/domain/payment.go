package domain

import "time"

type Payment struct {
	ID           string    `json:"id"`
	ServiceID    string    `json:"serviceId"`
	TechnicianID string    `json:"technicianId"`
	SettlementID *string   `json:"settlementId,omitempty"`
	Method       string    `json:"method"`
	AmountPaid   string    `json:"amountPaid"`
	DebtAmount   string    `json:"debtAmount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type PaymentFilter struct {
	Actor        ServiceActor
	TechnicianID string
	Page         int
	PageSize     int
}

type PaymentDraft struct {
	ServiceID    string
	TechnicianID string
	Method       string
	AmountPaid   string
	DebtAmount   string
}

type PaymentPatch struct {
	Method     Field[string]
	AmountPaid Field[string]
	DebtAmount Field[string]
}
