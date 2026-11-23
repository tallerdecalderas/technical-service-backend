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
	ErrCatalogForbidden = errors.New("catalog operation forbidden")
	ErrInvalidCatalog   = errors.New("invalid catalog input")
)

const (
	defaultCatalogPageSize = 25
	maxCatalogPageSize     = 100
	maxCatalogSearchRunes  = 200
)

type CatalogUseCases struct {
	repository outbound.CatalogRepository
}

type CatalogListInput struct {
	CompanyID       string
	Search          string
	IncludeInactive bool
	Page            int
	PageSize        int
}

func NewCatalogUseCases(repository outbound.CatalogRepository) *CatalogUseCases {
	return &CatalogUseCases{repository: repository}
}

func (s *CatalogUseCases) ListPrices(ctx context.Context, user domain.User, input CatalogListInput) (domain.CatalogPage, error) {
	actor, err := serviceActor(user)
	if err != nil {
		return domain.CatalogPage{}, ErrCatalogForbidden
	}
	if actor.Role != "ADMIN" && actor.Role != "TECHNICIAN" {
		return domain.CatalogPage{}, ErrCatalogForbidden
	}
	if input.IncludeInactive && actor.Role != "ADMIN" {
		return domain.CatalogPage{}, ErrCatalogForbidden
	}
	page, pageSize := input.Page, input.PageSize
	if page == 0 {
		page = 1
	}
	if pageSize == 0 {
		pageSize = defaultCatalogPageSize
	}
	search := strings.TrimSpace(input.Search)
	if page < 1 || pageSize < 1 || pageSize > maxCatalogPageSize ||
		page > int(^uint(0)>>1)/pageSize ||
		utf8.RuneCountInString(search) > maxCatalogSearchRunes {
		return domain.CatalogPage{}, ErrInvalidCatalog
	}
	if input.CompanyID != "" {
		if !validUUID(input.CompanyID) {
			return domain.CatalogPage{}, ErrInvalidCatalog
		}
		if !containsString(actor.CompanyIDs, input.CompanyID) {
			return domain.CatalogPage{}, ErrCatalogForbidden
		}
	}
	result, err := s.repository.ListCatalogPrices(ctx, domain.CatalogFilter{
		Actor: actor, CompanyID: input.CompanyID, Search: search,
		IncludeInactive: input.IncludeInactive, Page: page, PageSize: pageSize,
	})
	if err != nil {
		return domain.CatalogPage{}, err
	}
	if result.Data == nil {
		result.Data = []domain.CatalogPrice{}
	}
	return result, nil
}
