package domain

import "time"

// Company representa una empresa o sucursal del sistema.
type Company struct {
	ID        string
	Name      string
	IsActive  bool
	Address   string
	Phone     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CompanySummary es una vista ligera de empresa para listados.
type CompanySummary struct {
	ID   string
	Name string
}

// CompanyCreateInput contiene los datos para crear una empresa.
type CompanyCreateInput struct {
	Name    string
	Address string
	Phone   string
}

// CompanyUpdateInput contiene los campos editables de una empresa.
type CompanyUpdateInput struct {
	Name    *string
	Address *string
	Phone   *string
	IsActive *bool
}
