package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type ClientFilter struct {
	Search     string
	Page       int
	PageSize   int
	UserID     string
	Role       string
	CompanyIDs []string
}

type ClientRepository interface {
	ListClients(context.Context, ClientFilter) ([]domain.Client, int, error)
	GetClient(context.Context, string, ClientFilter) (domain.Client, []domain.ClientServiceRecord, error)
	CreateClient(context.Context, domain.ClientDraft) (domain.Client, error)
	UpdateClient(context.Context, string, domain.ClientPatch, ClientFilter) (domain.Client, error)
}
