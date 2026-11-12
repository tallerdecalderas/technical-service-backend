package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type CatalogRepository interface {
	ListCatalogPrices(ctx context.Context, filter domain.CatalogFilter) (domain.CatalogPage, error)
}
