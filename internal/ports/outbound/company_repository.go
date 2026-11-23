package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type CompanyRepository interface {
	ListActiveCompanies(context.Context, []string) ([]domain.CompanySummary, error)
}
