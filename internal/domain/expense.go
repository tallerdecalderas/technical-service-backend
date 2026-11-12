package domain

import "time"

type Expense struct {
	ID           string    `json:"id"`
	ServiceID    string    `json:"serviceId"`
	Concept      string    `json:"concept"`
	Amount       string    `json:"amount"`
	Currency     string    `json:"currency"`
	RecordedByID string    `json:"recordedById"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type ExpenseFilter struct {
	Actor     ServiceActor
	ServiceID string
}

type ExpenseDraft struct {
	ServiceID    string
	Concept      string
	Amount       string
	Currency     string
	RecordedByID string
}

type ExpensePatch struct {
	Concept  Field[string]
	Amount   Field[string]
	Currency Field[string]
}
