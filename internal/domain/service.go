package domain

import "time"

type ServiceActor struct {
	UserID     string
	Role       string
	CompanyIDs []string
}

type ServiceSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"isActive,omitempty"`
}

type ServiceUser struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Email  string  `json:"email,omitempty"`
	Phone  *string `json:"phone,omitempty"`
	DNI    *string `json:"dni,omitempty"`
	Role   string  `json:"role,omitempty"`
	Active bool    `json:"isActive,omitempty"`
	Avatar *string `json:"avatar,omitempty"`
}

type Service struct {
	ID                string         `json:"id"`
	OrderNumber       int            `json:"orderNumber"`
	CompanyID         string         `json:"companyId"`
	ClientID          string         `json:"clientId"`
	CategoryID        string         `json:"categoryId"`
	TechnicianID      *string        `json:"technicianId"`
	CreatedByID       string         `json:"createdById"`
	ClosedByID        *string        `json:"closedById"`
	Status            string         `json:"status"`
	ScheduledDate     time.Time      `json:"scheduledDate"`
	Address           string         `json:"address"`
	Locality          string         `json:"locality"`
	Observation       *string        `json:"observation"`
	ExpectedAmount    *string        `json:"expectedAmount"`
	FinalAmount       string         `json:"finalAmount"`
	CompletedPhotoURL *string        `json:"completedPhotoUrl"`
	CompletedAt       *time.Time     `json:"completedAt"`
	ClosedAt          *time.Time     `json:"closedAt"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	Company           ServiceSummary `json:"company"`
	Client            Client         `json:"client"`
	Category          ServiceSummary `json:"category"`
	Technician        *ServiceUser   `json:"technician"`
	CreatedBy         ServiceUser    `json:"createdBy"`
	ClosedBy          *ServiceUser   `json:"closedBy"`
}

type ServiceFilter struct {
	Actor        ServiceActor
	Status       string
	TechnicianID string
	ClientID     string
	CompanyID    string
	CategoryID   string
	DateFrom     *time.Time
	DateTo       *time.Time
	Page         int
	PageSize     int
}

type ServiceDraft struct {
	CompanyID      string
	ClientID       string
	CategoryID     string
	TechnicianID   *string
	CreatedByID    string
	ScheduledDate  time.Time
	Address        string
	Locality       string
	Observation    *string
	ExpectedAmount *string
	FinalAmount    string
}

type ServicePatch struct {
	CompanyID      Field[string]
	ClientID       Field[string]
	CategoryID     Field[string]
	TechnicianID   Field[string]
	ScheduledDate  Field[time.Time]
	Address        Field[string]
	Locality       Field[string]
	Observation    Field[string]
	ExpectedAmount Field[string]
	FinalAmount    Field[string]
}

type ServiceFinish struct {
	FinalAmount string
	Observation *string
}

type ActivityLog struct {
	ID          string      `json:"id"`
	ServiceID   string      `json:"serviceId"`
	UserID      string      `json:"userId"`
	Action      string      `json:"action"`
	Description string      `json:"description"`
	Metadata    any         `json:"metadata"`
	CreatedAt   time.Time   `json:"createdAt"`
	User        ServiceUser `json:"user"`
}

type ActivityRecord struct {
	ServiceID   string
	UserID      string
	Action      string
	Description string
	Metadata    []byte
}

type ServiceHistoryResult struct {
	Data     []ActivityLog `json:"data"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
	Total    int           `json:"total"`
}

type Field[T any] struct {
	Set   bool
	Value *T
}
