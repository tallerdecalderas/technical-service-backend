package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type EstimateRepository interface {
	ListEstimates(context.Context, domain.EstimateFilter) ([]domain.Estimate, int, error)
	GetEstimate(context.Context, string, domain.ServiceActor) (domain.Estimate, error)
	CreateEstimate(context.Context, domain.EstimateDraft, domain.ServiceActor) (domain.Estimate, error)
	FinalizeEstimate(context.Context, string, domain.ServiceActor) (domain.Estimate, error)
}
