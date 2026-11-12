package domain

import "time"

type Technician struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     *string   `json:"phone"`
	DNI       *string   `json:"dni"`
	Role      string    `json:"role"`
	Active    bool      `json:"isActive"`
	Avatar    *string   `json:"avatar"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TechnicianDraft struct {
	Name         string
	Email        string
	PasswordHash string
	Phone        *string
	DNI          *string
	CompanyIDs   []string
	IdentityID   string
}

type TechnicianPatch struct {
	Name       Field[string]
	Phone      Field[string]
	DNI        Field[string]
	Active     Field[bool]
	Avatar     Field[string]
	CompanyIDs Field[[]string]
}

type TechnicianFilter struct {
	UserID     string
	CompanyIDs []string
	SelfOnly   bool
	Page       int
	PageSize   int
}
