package application

import (
	"context"
	"errors"
	"strings"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var ErrCompanyForbidden = errors.New("company operation forbidden")

type CompanyUseCases struct {
	repository outbound.CompanyRepository
}

func NewCompanyUseCases(repository outbound.CompanyRepository) *CompanyUseCases {
	return &CompanyUseCases{repository: repository}
}

func (s *CompanyUseCases) List(ctx context.Context, user domain.User) ([]domain.CompanySummary, error) {
	if !user.Active || (!strings.EqualFold(user.Role, "ADMIN") && !strings.EqualFold(user.Role, "TECHNICIAN")) {
		return nil, ErrCompanyForbidden
	}
	if len(user.CompanyIDs) == 0 {
		return []domain.CompanySummary{}, nil
	}

	companies, err := s.repository.ListActiveCompanies(ctx, append([]string(nil), user.CompanyIDs...))
	if err != nil {
		return nil, err
	}
	if companies == nil {
		return []domain.CompanySummary{}, nil
	}
	return companies, nil
}
