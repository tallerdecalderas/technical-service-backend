package application

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrCatalogAdminForbidden = errors.New("catalog administration forbidden")
	ErrInvalidCatalogAdmin   = errors.New("invalid catalog administration input")
	ErrCatalogAdminNotFound  = outbound.ErrRecordNotFound
	ErrCatalogAdminConflict  = outbound.ErrConflict
	ErrCatalogAdminRule      = errors.New("catalog administration rule violation")
)

const (
	maxCatalogProviderNameRunes = 200
	maxCatalogMarginPercent     = "1000"
)

type CatalogAdminUseCases struct {
	repository outbound.CatalogAdminRepository
}

type CatalogProviderListInput struct {
	CompanyID string
	Search    string
	Page      int
	PageSize  int
}

type CatalogOfferListInput struct {
	CompanyID       string
	InventoryItemID string
	Page            int
	PageSize        int
}

func NewCatalogAdminUseCases(repository outbound.CatalogAdminRepository) *CatalogAdminUseCases {
	return &CatalogAdminUseCases{repository: repository}
}

func (s *CatalogAdminUseCases) ListProviders(ctx context.Context, user domain.User, input CatalogProviderListInput) (domain.CatalogProviderPage, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogProviderPage{}, err
	}
	page, pageSize, err := normalizeCatalogPagination(input.Page, input.PageSize)
	if err != nil {
		return domain.CatalogProviderPage{}, err
	}
	search := strings.TrimSpace(input.Search)
	if utf8.RuneCountInString(search) > maxCatalogSearchRunes {
		return domain.CatalogProviderPage{}, ErrInvalidCatalogAdmin
	}
	if input.CompanyID != "" {
		if !validUUID(input.CompanyID) || !containsString(actor.CompanyIDs, input.CompanyID) {
			return domain.CatalogProviderPage{}, ErrCatalogAdminForbidden
		}
	}
	result, err := s.repository.ListProviders(ctx, domain.CatalogProviderFilter{
		Actor: actor, CompanyID: input.CompanyID, Search: search, Page: page, PageSize: pageSize,
	})
	if err != nil {
		return domain.CatalogProviderPage{}, err
	}
	if result.Data == nil {
		result.Data = []domain.CatalogProvider{}
	}
	return result, nil
}

func (s *CatalogAdminUseCases) CreateProvider(ctx context.Context, user domain.User, companyID, name string) (domain.CatalogProvider, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogProvider{}, err
	}
	normalizedName, ok := normalizeProviderName(name)
	if !ok || !validUUID(companyID) || !containsString(actor.CompanyIDs, companyID) {
		return domain.CatalogProvider{}, ErrInvalidCatalogAdmin
	}
	return s.repository.CreateProvider(ctx, domain.CatalogProviderDraft{CompanyID: companyID, Name: normalizedName}, actor)
}

func (s *CatalogAdminUseCases) UpdateProvider(ctx context.Context, user domain.User, id string, patch domain.CatalogProviderPatch) (domain.CatalogProvider, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogProvider{}, err
	}
	if !validUUID(id) || (!patch.Name.Set && !patch.IsActive.Set) {
		return domain.CatalogProvider{}, ErrInvalidCatalogAdmin
	}
	if patch.Name.Set {
		if patch.Name.Value == nil {
			return domain.CatalogProvider{}, ErrInvalidCatalogAdmin
		}
		name, ok := normalizeProviderName(*patch.Name.Value)
		if !ok {
			return domain.CatalogProvider{}, ErrInvalidCatalogAdmin
		}
		patch.Name.Value = &name
	}
	return s.repository.UpdateProvider(ctx, id, patch, actor)
}

func (s *CatalogAdminUseCases) ListOffers(ctx context.Context, user domain.User, input CatalogOfferListInput) (domain.CatalogOfferPage, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogOfferPage{}, err
	}
	page, pageSize, err := normalizeCatalogPagination(input.Page, input.PageSize)
	if err != nil {
		return domain.CatalogOfferPage{}, err
	}
	if input.CompanyID != "" {
		if !validUUID(input.CompanyID) || !containsString(actor.CompanyIDs, input.CompanyID) {
			return domain.CatalogOfferPage{}, ErrCatalogAdminForbidden
		}
	}
	if input.InventoryItemID != "" && !validUUID(input.InventoryItemID) {
		return domain.CatalogOfferPage{}, ErrInvalidCatalogAdmin
	}
	result, err := s.repository.ListOffers(ctx, domain.CatalogOfferFilter{
		Actor: actor, CompanyID: input.CompanyID, InventoryItemID: input.InventoryItemID,
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		return domain.CatalogOfferPage{}, err
	}
	if result.Data == nil {
		result.Data = []domain.CatalogOffer{}
	}
	return result, nil
}

func (s *CatalogAdminUseCases) CreateOffer(ctx context.Context, user domain.User, companyID, itemID, providerID, costUsd string) (domain.CatalogOffer, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogOffer{}, err
	}
	if !validUUID(companyID) || !containsString(actor.CompanyIDs, companyID) ||
		!validUUID(itemID) || !validUUID(providerID) || !validNonNegativeDecimal(costUsd) {
		return domain.CatalogOffer{}, ErrInvalidCatalogAdmin
	}
	return s.repository.CreateOffer(ctx, domain.CatalogOfferDraft{
		CompanyID: companyID, InventoryItemID: itemID, ProviderID: providerID,
		CostUsd: strings.TrimSpace(costUsd),
	}, actor)
}

func (s *CatalogAdminUseCases) UpdateOffer(ctx context.Context, user domain.User, id string, patch domain.CatalogOfferPatch) (domain.CatalogOffer, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogOffer{}, err
	}
	if !validUUID(id) || (!patch.CostUsd.Set && !patch.IsActive.Set) {
		return domain.CatalogOffer{}, ErrInvalidCatalogAdmin
	}
	if patch.CostUsd.Set {
		if patch.CostUsd.Value == nil || !validNonNegativeDecimal(*patch.CostUsd.Value) {
			return domain.CatalogOffer{}, ErrInvalidCatalogAdmin
		}
		value := strings.TrimSpace(*patch.CostUsd.Value)
		patch.CostUsd.Value = &value
	}
	return s.repository.UpdateOffer(ctx, id, patch, actor)
}

func (s *CatalogAdminUseCases) LatestExchangeRate(ctx context.Context, user domain.User) (domain.ExchangeRate, error) {
	if _, err := catalogAdminActor(user); err != nil {
		return domain.ExchangeRate{}, err
	}
	return s.repository.LatestExchangeRate(ctx)
}

func (s *CatalogAdminUseCases) UpsertExchangeRate(ctx context.Context, user domain.User, rateDate, rate string) (domain.ExchangeRate, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.ExchangeRate{}, err
	}
	parsedDate, ok := parsePeriodDate(rateDate)
	if !ok || parsedDate.After(todayART()) || !validPositiveDecimal(rate) {
		return domain.ExchangeRate{}, ErrInvalidCatalogAdmin
	}
	return s.repository.UpsertExchangeRate(ctx, domain.ExchangeRateDraft{
		RateDate: parsedDate.Format(periodDateLayout), Rate: strings.TrimSpace(rate), CreatedByID: actor.UserID,
	}, actor)
}

func (s *CatalogAdminUseCases) UpdateItem(ctx context.Context, user domain.User, id string, patch domain.CatalogItemPatch) (domain.CatalogItemPrice, error) {
	actor, err := catalogAdminActor(user)
	if err != nil {
		return domain.CatalogItemPrice{}, err
	}
	if !validUUID(id) || (!patch.MarginPercent.Set && !patch.TechPrice.Set && !patch.IsActive.Set) {
		return domain.CatalogItemPrice{}, ErrInvalidCatalogAdmin
	}
	if patch.MarginPercent.Set {
		if patch.MarginPercent.Value == nil {
			return domain.CatalogItemPrice{}, ErrInvalidCatalogAdmin
		}
		value := strings.TrimSpace(*patch.MarginPercent.Value)
		if !validNonNegativeDecimal(value) {
			return domain.CatalogItemPrice{}, ErrInvalidCatalogAdmin
		}
		margin, err := parseDecimal(value)
		if err != nil {
			return domain.CatalogItemPrice{}, ErrInvalidCatalogAdmin
		}
		limit, _ := parseDecimal(maxCatalogMarginPercent)
		if margin.Cmp(limit) > 0 {
			return domain.CatalogItemPrice{}, ErrInvalidCatalogAdmin
		}
		patch.MarginPercent.Value = &value
	}
	if patch.TechPrice.Set {
		if patch.TechPrice.Value == nil || !validNonNegativeDecimal(*patch.TechPrice.Value) {
			return domain.CatalogItemPrice{}, ErrInvalidCatalogAdmin
		}
		value := strings.TrimSpace(*patch.TechPrice.Value)
		patch.TechPrice.Value = &value
	}
	return s.repository.UpdateItemPrice(ctx, id, patch, actor)
}

func catalogAdminActor(user domain.User) (domain.ServiceActor, error) {
	actor, err := serviceActor(user)
	if err != nil || actor.Role != "ADMIN" {
		return domain.ServiceActor{}, ErrCatalogAdminForbidden
	}
	return actor, nil
}

func normalizeCatalogPagination(page, pageSize int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = defaultCatalogPageSize
	}
	if page < 1 || pageSize < 1 || pageSize > maxCatalogPageSize {
		return 0, 0, ErrInvalidCatalogAdmin
	}
	return page, pageSize, nil
}

func normalizeProviderName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	if name == "" || utf8.RuneCountInString(name) > maxCatalogProviderNameRunes {
		return "", false
	}
	return name, true
}
