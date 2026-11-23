package outbound

import (
	"context"

	"technical-service-backend/internal/domain"
)

type UserRepository interface {
	CreateUser(context.Context, domain.UserCreateInput) (string, error)
	GetUser(context.Context, string) (*domain.User, error)
	ListUsers(context.Context, domain.User) ([]domain.UserSummary, error)
	UpdateUser(context.Context, string, domain.UserUpdateInput) error
	DeactivateUser(context.Context, string) error
}