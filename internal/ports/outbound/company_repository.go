package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type CompanyRepository interface {
	ListActiveCompanies(context.Context, []string) ([]domain.CompanySummary, error)
	CreateCompany(context.Context, domain.CompanyCreateInput) (string, error)
	GetCompany(context.Context, string) (*domain.Company, error)
	UpdateCompany(context.Context, string, domain.CompanyUpdateInput) error
	DeactivateCompany(context.Context, string) error
}
