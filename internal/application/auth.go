package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrInvalidCredentials   = outbound.ErrInvalidCredentials
	ErrUnauthenticated      = errors.New("unauthenticated")
	ErrIdentityNotLinked    = errors.New("identity is not linked")
	ErrAuthStoreUnavailable = errors.New("authentication storage unavailable")
	ErrInvalidResetRequest  = errors.New("invalid password reset request")
)

type Session struct {
	User        domain.User
	Memberships []domain.CompanyMembership
	Token       string    `json:"-"`
	CSRFToken   string    `json:"-"`
	ExpiresAt   time.Time `json:"expiresAt"`
	csrfHash    string
}

type AuthService struct {
	identity   outbound.IdentityProvider
	repository outbound.AuthRepository
	sessionTTL time.Duration
	now        func() time.Time
	random     io.Reader
}

func NewAuthService(identity outbound.IdentityProvider, repository outbound.AuthRepository, sessionTTL time.Duration) *AuthService {
	return &AuthService{
		identity:   identity,
		repository: repository,
		sessionTTL: sessionTTL,
		now:        time.Now,
		random:     rand.Reader,
	}
}

func (s *AuthService) Login(ctx context.Context, email, password string) (Session, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return Session{}, ErrInvalidCredentials
	}
	tokens, err := s.identity.Authenticate(ctx, email, password)
	if err != nil {
		return Session{}, err
	}
	claims, err := s.identity.ValidateAccessToken(ctx, tokens.AccessToken)
	if err != nil || claims.Subject == "" {
		return Session{}, ErrUnauthenticated
	}
	user, memberships, err := s.resolveActiveUser(ctx, claims.Subject)
	if err != nil {
		return Session{}, err
	}

	rawSession, err := randomToken(s.random)
	if err != nil {
		return Session{}, fmt.Errorf("generate session token: %w", err)
	}
	rawCSRF, err := randomToken(s.random)
	if err != nil {
		return Session{}, fmt.Errorf("generate CSRF token: %w", err)
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.sessionTTL)
	record := domain.AuthSession{
		TokenHash:     hashSecret(rawSession),
		CSRFTokenHash: hashSecret(rawCSRF),
		UserID:        user.ID,
		CreatedAt:     now,
		ExpiresAt:     expiresAt,
	}
	if err := s.repository.CreateSession(ctx, record); err != nil {
		return Session{}, fmt.Errorf("%w: create session", ErrAuthStoreUnavailable)
	}
	return Session{
		User:        user,
		Memberships: memberships,
		Token:       rawSession,
		CSRFToken:   rawCSRF,
		ExpiresAt:   expiresAt,
		csrfHash:    record.CSRFTokenHash,
	}, nil
}

func (s *AuthService) Session(ctx context.Context, rawToken string) (Session, error) {
	if rawToken == "" {
		return Session{}, ErrUnauthenticated
	}
	record, err := s.repository.FindSession(ctx, hashSecret(rawToken))
	if errors.Is(err, outbound.ErrRecordNotFound) {
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, fmt.Errorf("%w: find session", ErrAuthStoreUnavailable)
	}
	now := s.now()
	if record.RevokedAt != nil || !record.ExpiresAt.After(now) ||
		record.User.ID == "" || !record.User.Active || !hasActiveMembership(record.Memberships) {
		return Session{}, ErrUnauthenticated
	}
	activeMemberships := activeMemberships(record.Memberships)
	if err := s.repository.UpdateLastSeen(ctx, record.TokenHash, now.UTC()); err != nil {
		return Session{}, fmt.Errorf("%w: update session activity", ErrAuthStoreUnavailable)
	}
	return Session{
		User:        record.User,
		Memberships: activeMemberships,
		ExpiresAt:   record.ExpiresAt,
		csrfHash:    record.CSRFTokenHash,
	}, nil
}

func (s *AuthService) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	if err := s.repository.RevokeSession(ctx, hashSecret(rawToken), s.now().UTC()); err != nil {
		return fmt.Errorf("%w: revoke session", ErrAuthStoreUnavailable)
	}
	return nil
}

func (s *AuthService) ValidateCSRF(session Session, token string) bool {
	if token == "" || session.csrfHash == "" {
		return false
	}
	presentedHash := hashSecret(token)
	return subtle.ConstantTimeCompare([]byte(presentedHash), []byte(session.csrfHash)) == 1
}

func (s *AuthService) RequestPasswordReset(ctx context.Context, email string) error {
	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return ErrInvalidResetRequest
	}
	if err := s.identity.RequestPasswordReset(ctx, email); err != nil {
		return err
	}
	return nil
}

func (s *AuthService) ResetPassword(ctx context.Context, accessToken, newPassword string) error {
	if strings.TrimSpace(accessToken) == "" || len(newPassword) < 8 {
		return ErrInvalidResetRequest
	}
	claims, err := s.identity.ValidateAccessToken(ctx, accessToken)
	if err != nil || claims.Subject == "" {
		return ErrInvalidResetRequest
	}
	if _, _, err := s.resolveActiveUser(ctx, claims.Subject); err != nil {
		return ErrInvalidResetRequest
	}
	if err := s.identity.ResetPassword(ctx, accessToken, newPassword); err != nil {
		return err
	}
	return nil
}

func (s *AuthService) resolveActiveUser(ctx context.Context, subject string) (domain.User, []domain.CompanyMembership, error) {
	user, memberships, err := s.repository.ResolveUser(ctx, "supabase", subject)
	if errors.Is(err, outbound.ErrRecordNotFound) {
		return domain.User{}, nil, ErrUnauthenticated
	}
	if err != nil {
		return domain.User{}, nil, fmt.Errorf("%w: resolve linked user", ErrAuthStoreUnavailable)
	}
	if user.ID == "" || !user.Active || !hasActiveMembership(memberships) {
		return domain.User{}, nil, ErrUnauthenticated
	}
	return user, activeMemberships(memberships), nil
}

func hasActiveMembership(memberships []domain.CompanyMembership) bool {
	for _, membership := range memberships {
		if membership.Active && membership.CompanyID != "" {
			return true
		}
	}
	return false
}

func activeMemberships(memberships []domain.CompanyMembership) []domain.CompanyMembership {
	active := make([]domain.CompanyMembership, 0, len(memberships))
	for _, membership := range memberships {
		if membership.Active && membership.CompanyID != "" {
			active = append(active, membership)
		}
	}
	return active
}

func randomToken(reader io.Reader) (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashSecret(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
