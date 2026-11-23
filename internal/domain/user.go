package domain

import "time"

// UserCreateInput contiene los datos para crear un usuario interno.
type UserCreateInput struct {
	Name     string
	Email    string
	Password string
	Role     string
	Phone    string
	DNI      string
	CompanyID string
}

// UserUpdateInput contiene los campos editables de un usuario.
type UserUpdateInput struct {
	Name     *string
	Email    *string
	Role     *string
	Phone    *string
	DNI      *string
	IsActive *bool
	CompanyID *string
}

// UserSummary es una vista ligera de usuario para listados.
type UserSummary struct {
	ID         string
	Name       string
	Email      string
	Role       string
	Phone      string
	DNI        string
	CompanyID  string
	CompanyName string
	IsActive   bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
