package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type TechnicianRepository interface {
	ListTechnicians(context.Context, domain.TechnicianFilter) ([]domain.Technician, int, error)
	GetTechnician(context.Context, string, domain.TechnicianFilter) (domain.Technician, error)
	CreateTechnician(context.Context, domain.TechnicianDraft) (domain.Technician, error)
	UpdateTechnician(context.Context, string, domain.TechnicianPatch, []string) (domain.Technician, error)
}

type UserProvisioner interface {
	ProvisionUser(context.Context, string, string) (string, error)
}
