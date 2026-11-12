package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type ServicePartRepository interface {
	ListServiceParts(context.Context, string, domain.ServiceActor) ([]domain.ServicePart, error)
	CreateServicePart(context.Context, domain.ServicePartDraft, domain.ServiceActor) (domain.ServicePart, error)
	UpdateServicePart(context.Context, string, string, domain.ServicePartPatch, domain.ServiceActor) (domain.ServicePart, error)
	DeleteServicePart(context.Context, string, string, domain.ServiceActor) error
}
