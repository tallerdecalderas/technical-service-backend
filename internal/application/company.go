package application

import (
	"context"
	"errors"
	"strings"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrCompanyForbidden = errors.New("company operation forbidden")
	ErrInvalidCompany   = errors.New("invalid company input")
)

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

func (s *CompanyUseCases) Create(ctx context.Context, user domain.User, input domain.CompanyCreateInput) (domain.CompanySummary, error) {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return domain.CompanySummary{}, ErrCompanyForbidden
	}
	if strings.TrimSpace(input.Name) == "" {
		return domain.CompanySummary{}, ErrInvalidCompany
	}

	id, err := s.repository.CreateCompany(ctx, input)
	if err != nil {
		return domain.CompanySummary{}, err
	}
	return domain.CompanySummary{ID: id, Name: strings.TrimSpace(input.Name)}, nil
}

func (s *CompanyUseCases) Update(ctx context.Context, user domain.User, id string, input domain.CompanyUpdateInput) (domain.CompanySummary, error) {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return domain.CompanySummary{}, ErrCompanyForbidden
	}
	if id == "" {
		return domain.CompanySummary{}, ErrInvalidCompany
	}

	current, err := s.repository.GetCompany(ctx, id)
	if err != nil {
		return domain.CompanySummary{}, err
	}

	name := current.Name
	if input.Name != nil {
		if strings.TrimSpace(*input.Name) == "" {
			return domain.CompanySummary{}, ErrInvalidCompany
		}
		name = strings.TrimSpace(*input.Name)
	}

	if err := s.repository.UpdateCompany(ctx, id, input); err != nil {
		return domain.CompanySummary{}, err
	}

	return domain.CompanySummary{ID: current.ID, Name: name}, nil
}

func (s *CompanyUseCases) Deactivate(ctx context.Context, user domain.User, id string) error {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return ErrCompanyForbidden
	}
	if id == "" {
		return ErrInvalidCompany
	}
	return s.repository.DeactivateCompany(ctx, id)
}
