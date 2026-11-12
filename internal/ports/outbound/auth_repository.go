package outbound

import (
	"context"
	"time"

	"technical-service-backend/internal/domain"
)

type AuthRepository interface {
	ResolveUser(ctx context.Context, provider, subject string) (domain.User, []domain.CompanyMembership, error)
	CreateSession(ctx context.Context, session domain.AuthSession) error
	FindSession(ctx context.Context, tokenHash string) (domain.AuthSession, error)
	RevokeSession(ctx context.Context, tokenHash string, at time.Time) error
	UpdateLastSeen(ctx context.Context, tokenHash string, at time.Time) error
}
