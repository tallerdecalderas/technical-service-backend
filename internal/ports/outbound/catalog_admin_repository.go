package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type CatalogAdminRepository interface {
	ListProviders(ctx context.Context, filter domain.CatalogProviderFilter) (domain.CatalogProviderPage, error)
	CreateProvider(ctx context.Context, draft domain.CatalogProviderDraft, actor domain.ServiceActor) (domain.CatalogProvider, error)
	UpdateProvider(ctx context.Context, id string, patch domain.CatalogProviderPatch, actor domain.ServiceActor) (domain.CatalogProvider, error)
	ListOffers(ctx context.Context, filter domain.CatalogOfferFilter) (domain.CatalogOfferPage, error)
	CreateOffer(ctx context.Context, draft domain.CatalogOfferDraft, actor domain.ServiceActor) (domain.CatalogOffer, error)
	UpdateOffer(ctx context.Context, id string, patch domain.CatalogOfferPatch, actor domain.ServiceActor) (domain.CatalogOffer, error)
	LatestExchangeRate(ctx context.Context) (domain.ExchangeRate, error)
	UpsertExchangeRate(ctx context.Context, draft domain.ExchangeRateDraft, actor domain.ServiceActor) (domain.ExchangeRate, error)
	UpdateItemPrice(ctx context.Context, itemID string, patch domain.CatalogItemPatch, actor domain.ServiceActor) (domain.CatalogItemPrice, error)
}

type CatalogImportRepository interface {
	PublishCatalog(ctx context.Context, input domain.CatalogImportPublishInput) (domain.CatalogImport, error)
	RecordFailedCatalogImport(ctx context.Context, failure domain.CatalogImportFailure) (domain.CatalogImport, error)
}

type CatalogSheetReader interface {
	ReadCatalogRows(ctx context.Context) ([][]string, string, error)
}
