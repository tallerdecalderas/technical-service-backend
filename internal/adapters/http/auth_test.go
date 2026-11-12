package httpadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

func TestAuthLoginWritesAuditEventWithoutSessionSecrets(t *testing.T) {
	var logs bytes.Buffer
	handler := NewAuthHandler(&fakeAuthService{loginSession: application.Session{
		User:  domain.User{ID: "user-1", Email: "user@example.com", Role: "ADMIN"},
		Token: "session-secret", CSRFToken: "csrf-secret", ExpiresAt: time.Now().Add(time.Hour),
	}}, testAuthConfig(), slog.New(slog.NewTextHandler(&logs, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	output := logs.String()
	for _, expected := range []string{"authentication audit event", "login_succeeded", "user-1"} {
		if !strings.Contains(output, expected) {
			t.Errorf("audit log does not contain %q: %s", expected, output)
		}
	}
	for _, secret := range []string{"session-secret", "csrf-secret", "user@example.com", "password"} {
		if strings.Contains(output, secret) {
			t.Errorf("audit log exposed %q: %s", secret, output)
		}
	}
}

func TestAuthLoginPreservesProviderRateLimitResponse(t *testing.T) {
	handler := NewAuthHandler(
		&fakeAuthService{loginErr: outbound.ErrRateLimited},
		testAuthConfig(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want too many requests", response.Code)
	}
}

func TestAuthLoginSetsOpaqueHttpOnlySessionCookieAndDoesNotReturnTokens(t *testing.T) {
	service := &fakeAuthService{loginSession: application.Session{
		User:  domain.User{ID: "user-1", Email: "user@example.com", Name: "User", Role: "ADMIN", Active: true},
		Token: "opaque-session", CSRFToken: "csrf-value", ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewAuthHandler(service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies = %#v, want session and CSRF cookies", cookies)
	}
	if cookies[0].Name != "tech-service-session" || !cookies[0].HttpOnly || cookies[0].Value != "opaque-session" {
		t.Errorf("session cookie = %#v", cookies[0])
	}
	if cookies[1].Name != "tech-service-csrf" || cookies[1].HttpOnly || cookies[1].Value != "csrf-value" {
		t.Errorf("CSRF cookie = %#v", cookies[1])
	}
	if strings.Contains(response.Body.String(), "opaque-session") || strings.Contains(response.Body.String(), "csrf-value") || strings.Contains(response.Body.String(), "access_token") {
		t.Errorf("response body exposed a token: %s", response.Body.String())
	}
}

func TestNewHandlerWithAuthRegistersAuthRoutes(t *testing.T) {
	service := &fakeAuthService{loginSession: application.Session{
		User:  domain.User{ID: "user-1", Email: "user@example.com", Role: "ADMIN"},
		Token: "session", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour),
	}}
	handler := NewHandlerWithAuth(
		healthCheckerFunc(func(context.Context) error { return nil }),
		[]string{"http://localhost:3000"},
		testAuthConfig(),
		service,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Error("auth response is missing X-Request-ID")
	}
}

func TestAuthLoginUsesSecureSameSiteCookiesOutsideDevelopment(t *testing.T) {
	cfg := testAuthConfig()
	cfg.SecureCookies = true
	handler := NewAuthHandler(&fakeAuthService{loginSession: application.Session{
		User:  domain.User{ID: "user-1", Email: "user@example.com", Role: "ADMIN"},
		Token: "session", CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour),
	}}, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password"}`))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("Origin", "http://localhost:3000")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	cookies := response.Result().Cookies()
	if response.Code != http.StatusOK || len(cookies) != 2 {
		t.Fatalf("status = %d, cookies = %#v", response.Code, cookies)
	}
	for _, cookie := range cookies {
		if !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie flags = %#v", cookie)
		}
	}
}

func TestAuthMutationRejectsUntrustedOrigin(t *testing.T) {
	service := &fakeAuthService{}
	handler := NewAuthHandler(service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"user@example.com","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want forbidden", response.Code)
	}
	if service.loginCalls != 0 {
		t.Fatalf("auth service calls = %d", service.loginCalls)
	}
}

func TestAuthLogoutRequiresMatchingCsrfCookieAndHeader(t *testing.T) {
	handler := NewAuthHandler(&fakeAuthService{}, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("X-CSRF-Token", "wrong")
	request.AddCookie(&http.Cookie{Name: "tech-service-session", Value: "session"})
	request.AddCookie(&http.Cookie{Name: "tech-service-csrf", Value: "csrf"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want forbidden", response.Code)
	}
}

func TestAuthLoginDoesNotExposeProviderOrIdentityDetails(t *testing.T) {
	service := &fakeAuthService{loginErr: errors.New("internal user lookup failed for private@example.com")}
	handler := NewAuthHandler(service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"private@example.com","password":"password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want internal server error", response.Code)
	}
	if strings.Contains(response.Body.String(), "private@example.com") || strings.Contains(response.Body.String(), "internal user lookup") {
		t.Errorf("response leaked internal auth details: %s", response.Body.String())
	}
}

func TestAuthSessionClearsStaleCookies(t *testing.T) {
	service := &fakeAuthService{sessionErr: application.ErrUnauthenticated}
	handler := NewAuthHandler(service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: "tech-service-session", Value: "stale"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
	if len(response.Result().Cookies()) != 2 {
		t.Fatalf("cookies = %#v, want stale session and CSRF cookies cleared", response.Result().Cookies())
	}
}

func TestAuthSessionRejectsInvalidCSRFHeaderWhenProvided(t *testing.T) {
	service := &fakeAuthService{loginSession: application.Session{
		User: domain.User{ID: "user-1", Email: "user@example.com", Role: "ADMIN"},
	}}
	handler := NewAuthHandler(service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: "tech-service-session", Value: "session"})
	request.AddCookie(&http.Cookie{Name: "tech-service-csrf", Value: "csrf"})
	request.Header.Set("X-CSRF-Token", "mismatch")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want forbidden", response.Code)
	}
}

type fakeAuthService struct {
	loginSession application.Session
	loginErr     error
	sessionErr   error
	logoutErr    error
	resetErr     error
	loginCalls   int
	csrfValid    bool
}

func (f *fakeAuthService) Login(context.Context, string, string) (application.Session, error) {
	f.loginCalls++
	return f.loginSession, f.loginErr
}
func (f *fakeAuthService) Session(context.Context, string) (application.Session, error) {
	return f.loginSession, f.sessionErr
}
func (f *fakeAuthService) ValidateCSRF(application.Session, string) bool    { return f.csrfValid }
func (f *fakeAuthService) Logout(context.Context, string) error             { return f.logoutErr }
func (*fakeAuthService) RequestPasswordReset(context.Context, string) error { return nil }
func (f *fakeAuthService) ResetPassword(context.Context, string, string) error {
	return f.resetErr
}

func testAuthConfig() config.AuthConfig {
	return config.AuthConfig{
		SessionCookieName: "tech-service-session",
		CSRFCookieName:    "tech-service-csrf",
		SessionTTL:        time.Hour,
		TrustedOrigins:    []string{"http://localhost:3000"},
	}
}
