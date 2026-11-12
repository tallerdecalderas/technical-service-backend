package domain

import "time"

type Estimate struct {
	ID          string     `json:"id"`
	ServiceID   string     `json:"serviceId"`
	Amount      string     `json:"amount"`
	Description *string    `json:"description,omitempty"`
	Notes       *string    `json:"notes,omitempty"`
	Status      string     `json:"status"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

type EstimateFilter struct {
	Actor        ServiceActor
	Status       string
	TechnicianID string
	Page         int
	PageSize     int
}

type EstimateDraft struct {
	ServiceID   string
	Amount      string
	Description *string
	Notes       *string
}
