package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type ServiceRepository interface {
	ListServices(context.Context, domain.ServiceFilter) ([]domain.Service, int, error)
	GetService(context.Context, string, domain.ServiceActor) (domain.Service, error)
	CreateService(context.Context, domain.ServiceDraft) (domain.Service, error)
	UpdateService(context.Context, string, domain.ServicePatch, domain.ServiceActor) (domain.Service, error)
	ChangeServiceStatus(context.Context, string, string, string, domain.ServiceActor) (domain.Service, error)
	FinishService(context.Context, string, domain.ServiceFinish, domain.ServiceActor) (domain.Service, error)
	ListServiceHistory(context.Context, string, domain.ServiceActor, int, int) ([]domain.ActivityLog, int, error)
}
