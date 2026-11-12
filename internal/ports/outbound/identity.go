package outbound

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidCredentials          = errors.New("invalid credentials")
	ErrIdentityProviderUnavailable = errors.New("identity provider unavailable")
	ErrInvalidAccessToken          = errors.New("invalid access token")
	ErrRecordNotFound              = errors.New("record not found")
	ErrConflict                    = errors.New("record conflict")
	ErrRuleViolation               = errors.New("domain rule violation")
	ErrRateLimited                 = errors.New("authentication rate limit exceeded")
)

type IdentityProvider interface {
	Authenticate(ctx context.Context, email, password string) (IdentityTokens, error)
	ValidateAccessToken(ctx context.Context, accessToken string) (IdentityClaims, error)
	RequestPasswordReset(ctx context.Context, email string) error
	ResetPassword(ctx context.Context, accessToken, newPassword string) error
}

type IdentityTokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type IdentityClaims struct {
	Subject   string
	Issuer    string
	Audience  string
	Email     string
	ExpiresAt time.Time
}
