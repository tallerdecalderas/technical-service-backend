package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type SettlementRepository interface {
	ListSettlements(ctx context.Context, filter domain.SettlementFilter) (domain.SettlementPage, error)
	GetSettlement(ctx context.Context, id string, actor domain.ServiceActor) (domain.Settlement, error)
	GenerateSettlement(ctx context.Context, draft domain.SettlementDraft, actor domain.ServiceActor) (domain.Settlement, error)
	UpdateSettlement(ctx context.Context, id string, patch domain.SettlementPatch, actor domain.ServiceActor) (domain.Settlement, error)
	LiquidateSettlement(ctx context.Context, id string, actor domain.ServiceActor) (domain.Settlement, error)
}
