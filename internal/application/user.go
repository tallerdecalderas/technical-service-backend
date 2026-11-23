package application

import (
	"context"
	"errors"
	"strings"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrUserForbidden = errors.New("user operation forbidden")
	ErrInvalidUser   = errors.New("invalid user input")
)

type UserUseCases struct {
	repository outbound.UserRepository
}

func NewUserUseCases(repository outbound.UserRepository) *UserUseCases {
	return &UserUseCases{repository: repository}
}

func (s *UserUseCases) List(ctx context.Context, user domain.User) ([]domain.UserSummary, error) {
	if !user.Active || (!strings.EqualFold(user.Role, "ADMIN") && !strings.EqualFold(user.Role, "TECHNICIAN")) {
		return nil, ErrUserForbidden
	}
	if len(user.CompanyIDs) == 0 {
		return []domain.UserSummary{}, nil
	}

	users, err := s.repository.ListUsers(ctx, user)
	if err != nil {
		return nil, err
	}
	if users == nil {
		users = []domain.UserSummary{}
	}
	return users, nil
}

func (s *UserUseCases) Create(ctx context.Context, user domain.User, input domain.UserCreateInput) (string, error) {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return "", ErrUserForbidden
	}
	if strings.TrimSpace(input.Name) == "" {
		return "", ErrInvalidUser
	}
	if strings.TrimSpace(input.Email) == "" {
		return "", ErrInvalidUser
	}
	if strings.TrimSpace(input.Password) == "" {
		return "", ErrInvalidUser
	}
	return s.repository.CreateUser(ctx, input)
}

func (s *UserUseCases) Get(ctx context.Context, user domain.User, id string) (*domain.User, error) {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return nil, ErrUserForbidden
	}
	if id == "" {
		return nil, errors.New("user ID is required")
	}
	return s.repository.GetUser(ctx, id)
}

func (s *UserUseCases) Update(ctx context.Context, user domain.User, id string, input domain.UserUpdateInput) error {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return ErrUserForbidden
	}
	if id == "" {
		return errors.New("user ID is required")
	}
	return s.repository.UpdateUser(ctx, id, input)
}

func (s *UserUseCases) Deactivate(ctx context.Context, user domain.User, id string) error {
	if !user.Active || !strings.EqualFold(user.Role, "ADMIN") {
		return ErrUserForbidden
	}
	if id == "" {
		return errors.New("user ID is required")
	}
	return s.repository.DeactivateUser(ctx, id)
}