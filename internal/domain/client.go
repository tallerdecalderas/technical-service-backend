package domain

import "time"

type Client struct {
	ID        string                `json:"id"`
	Name      *string               `json:"name"`
	Email     *string               `json:"email"`
	Phone     string                `json:"phone"`
	Address   *string               `json:"address"`
	Locality  *string               `json:"locality"`
	CreatedAt time.Time             `json:"createdAt"`
	UpdatedAt time.Time             `json:"updatedAt"`
	Services  []ClientServiceRecord `json:"services,omitempty"`
}

type ClientServiceRecord struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	ScheduledDate time.Time `json:"scheduledDate"`
}

type ClientDraft struct {
	Name     *string
	Email    *string
	Phone    string
	Address  *string
	Locality *string
}

type ClientField struct {
	Set   bool
	Value *string
}

type ClientPatch struct {
	Name     ClientField
	Email    ClientField
	Phone    ClientField
	Address  ClientField
	Locality ClientField
}
