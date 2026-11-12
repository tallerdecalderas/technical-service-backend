package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

func TestLoginRejectsMissingCredentials(t *testing.T) {
	service := NewAuthService(&fakeIdentityProvider{}, &fakeAuthRepository{}, time.Hour)

	if _, err := service.Login(context.Background(), " ", ""); !errors.Is(err, outbound.ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginStoresOnlyHashedSessionSecretsForLinkedActiveUser(t *testing.T) {
	provider := &fakeIdentityProvider{}
	repository := &fakeAuthRepository{
		user:        domain.User{ID: "user-1", Email: "user@example.com", Name: "User", Role: "ADMIN", Active: true},
		memberships: []domain.CompanyMembership{{CompanyID: "company-1", Active: true}},
	}
	service := NewAuthService(provider, repository, time.Hour)
	service.random = stringsReader("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	session, err := service.Login(context.Background(), "user@example.com", "password")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if session.Token == "" || session.CSRFToken == "" || session.ExpiresAt.IsZero() {
		t.Fatalf("Login() returned incomplete session: %#v", session)
	}
	if repository.created.TokenHash == session.Token || repository.created.CSRFTokenHash == session.CSRFToken {
		t.Fatal("repository stored a raw session or CSRF token")
	}
	if repository.resolveProvider != "supabase" || repository.resolveSubject != "provider-subject" {
		t.Errorf("ResolveUser() = (%q, %q)", repository.resolveProvider, repository.resolveSubject)
	}
	if !service.ValidateCSRF(session, session.CSRFToken) {
		t.Error("ValidateCSRF() rejected the issued token")
	}
	if service.ValidateCSRF(session, "attacker-token") {
		t.Error("ValidateCSRF() accepted a mismatched token")
	}
}

func TestLoginRejectsUnlinkedUserWithoutSearchingByEmail(t *testing.T) {
	repository := &fakeAuthRepository{resolveErr: outbound.ErrRecordNotFound}
	service := NewAuthService(&fakeIdentityProvider{}, repository, time.Hour)

	if _, err := service.Login(context.Background(), "user@example.com", "password"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Login() error = %v, want ErrUnauthenticated", err)
	}
	if repository.createCalls != 0 {
		t.Errorf("CreateSession() calls = %d, want 0", repository.createCalls)
	}
	if repository.resolveSubject != "provider-subject" {
		t.Errorf("resolved subject = %q, want verified provider subject", repository.resolveSubject)
	}
}

func TestLoginRejectsInactiveUserAndMissingActiveMembership(t *testing.T) {
	for _, tt := range []struct {
		name        string
		user        domain.User
		memberships []domain.CompanyMembership
	}{
		{name: "inactive user", user: domain.User{ID: "user-1", Active: false}, memberships: []domain.CompanyMembership{{CompanyID: "company-1", Active: true}}},
		{name: "no active memberships", user: domain.User{ID: "user-1", Active: true}, memberships: []domain.CompanyMembership{{CompanyID: "company-1", Active: false}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repository := &fakeAuthRepository{user: tt.user, memberships: tt.memberships}
			service := NewAuthService(&fakeIdentityProvider{}, repository, time.Hour)
			if _, err := service.Login(context.Background(), "user@example.com", "password"); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Login() error = %v, want ErrUnauthenticated", err)
			}
			if repository.createCalls != 0 {
				t.Errorf("CreateSession() calls = %d, want 0", repository.createCalls)
			}
		})
	}
}

func TestSessionRejectsExpiredOrRevokedRecord(t *testing.T) {
	now := time.Now().UTC()
	for _, tt := range []struct {
		name    string
		expires time.Time
		revoked *time.Time
	}{
		{name: "expired", expires: now.Add(-time.Second)},
		{name: "revoked", expires: now.Add(time.Hour), revoked: &now},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repository := &fakeAuthRepository{record: domain.AuthSession{
				User:        domain.User{ID: "user-1", Active: true},
				Memberships: []domain.CompanyMembership{{CompanyID: "company-1", Active: true}},
				ExpiresAt:   tt.expires, RevokedAt: tt.revoked,
				CSRFTokenHash: hashSecret("csrf"),
			}}
			service := NewAuthService(&fakeIdentityProvider{}, repository, time.Hour)
			service.now = func() time.Time { return now }
			if _, err := service.Session(context.Background(), "session"); !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Session() error = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestLogoutRevokesSessionHash(t *testing.T) {
	repository := &fakeAuthRepository{}
	service := NewAuthService(&fakeIdentityProvider{}, repository, time.Hour)
	if err := service.Logout(context.Background(), "opaque-session-token"); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if repository.revokedHash != hashSecret("opaque-session-token") {
		t.Errorf("revoked hash = %q, want hash of presented token", repository.revokedHash)
	}
}

type fakeIdentityProvider struct {
	authenticateErr error
	validateErr     error
}

func (f *fakeIdentityProvider) Authenticate(context.Context, string, string) (outbound.IdentityTokens, error) {
	return outbound.IdentityTokens{AccessToken: "access-token"}, f.authenticateErr
}
func (f *fakeIdentityProvider) ValidateAccessToken(context.Context, string) (outbound.IdentityClaims, error) {
	return outbound.IdentityClaims{Subject: "provider-subject"}, f.validateErr
}
func (*fakeIdentityProvider) RequestPasswordReset(context.Context, string) error  { return nil }
func (*fakeIdentityProvider) ResetPassword(context.Context, string, string) error { return nil }

type fakeAuthRepository struct {
	user            domain.User
	memberships     []domain.CompanyMembership
	resolveErr      error
	record          domain.AuthSession
	created         domain.AuthSession
	resolveProvider string
	resolveSubject  string
	revokedHash     string
	createCalls     int
}

func (f *fakeAuthRepository) ResolveUser(_ context.Context, provider, subject string) (domain.User, []domain.CompanyMembership, error) {
	f.resolveProvider, f.resolveSubject = provider, subject
	return f.user, f.memberships, f.resolveErr
}
func (f *fakeAuthRepository) CreateSession(_ context.Context, record domain.AuthSession) error {
	f.created = record
	f.createCalls++
	return nil
}
func (f *fakeAuthRepository) FindSession(context.Context, string) (domain.AuthSession, error) {
	return f.record, nil
}
func (f *fakeAuthRepository) RevokeSession(_ context.Context, tokenHash string, _ time.Time) error {
	f.revokedHash = tokenHash
	return nil
}
func (f *fakeAuthRepository) UpdateLastSeen(context.Context, string, time.Time) error { return nil }

func stringsReader(value string) *strings.Reader { return strings.NewReader(value) }

var _ outbound.AuthRepository = (*fakeAuthRepository)(nil)
var _ outbound.IdentityProvider = (*fakeIdentityProvider)(nil)
