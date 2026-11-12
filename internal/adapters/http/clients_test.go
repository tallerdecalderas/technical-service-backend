package httpadapter

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type clientUseCasesStub struct {
	listInput   application.ClientListInput
	listCalls   int
	createCalls int
}

func (s *clientUseCasesStub) List(_ context.Context, _ domain.User, input application.ClientListInput) (application.ClientListResult, error) {
	s.listCalls++
	s.listInput = input
	return application.ClientListResult{Data: []domain.Client{}, Page: input.Page, PageSize: input.PageSize, Total: 0}, nil
}

func (*clientUseCasesStub) Get(context.Context, domain.User, string) (domain.Client, error) {
	return domain.Client{}, application.ErrClientNotFound
}

func (s *clientUseCasesStub) Create(_ context.Context, _ domain.User, draft domain.ClientDraft) (domain.Client, error) {
	s.createCalls++
	return domain.Client{Phone: draft.Phone}, nil
}

func (*clientUseCasesStub) Update(context.Context, domain.User, string, domain.ClientPatch) (domain.Client, error) {
	return domain.Client{}, application.ErrClientNotFound
}

type fakeIdempotencyRepository struct{}

func (f *fakeIdempotencyRepository) Claim(_ context.Context, key outbound.IdempotencyKey) (outbound.IdempotencyRecord, bool, error) {
	return outbound.IdempotencyRecord{
		RequestHash: key.RequestHash,
		Completed:   false,
	}, true, nil
}

func (f *fakeIdempotencyRepository) Complete(_ context.Context, _ outbound.IdempotencyKey, _ int, _ string, _ []byte) error {
	return nil
}

func (f *fakeIdempotencyRepository) Release(_ context.Context, _ outbound.IdempotencyKey) error {
	return nil
}

func clientTestAuth(sessionErr error) *fakeAuthService {
	return &fakeAuthService{
		loginSession: application.Session{
			User: domain.User{ID: "admin-id", Role: "ADMIN", Active: true},
		},
		sessionErr: sessionErr,
		csrfValid:  true,
	}
}

func TestClientHandlerRequiresSession(t *testing.T) {
	service := &clientUseCasesStub{}
	auth := clientTestAuth(application.ErrUnauthenticated)
	handler := NewClientHandler(auth, service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if service.listCalls != 0 {
		t.Errorf("List calls = %d, want 0", service.listCalls)
	}
}

func TestClientHandlerPaginatesAdminList(t *testing.T) {
	service := &clientUseCasesStub{}
	auth := clientTestAuth(nil)
	handler := NewClientHandler(auth, service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/clients?search=smith&page=2&pageSize=10", nil)
	request.AddCookie(&http.Cookie{Name: "tech-service-session", Value: "session"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.listInput.Search != "smith" || service.listInput.Page != 2 || service.listInput.PageSize != 10 {
		t.Errorf("list input = %+v", service.listInput)
	}
	if !strings.Contains(response.Body.String(), `"meta":{"page":2,"pageSize":10,"total":0}`) {
		t.Errorf("body = %q, want pagination metadata", response.Body.String())
	}
}

func TestClientHandlerMutationsRequireOriginAndCSRF(t *testing.T) {
	service := &clientUseCasesStub{}
	auth := clientTestAuth(nil)
	handler := NewClientHandler(auth, service, testAuthConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{"phone":"555-0100"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	request.AddCookie(&http.Cookie{Name: "tech-service-session", Value: "session"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if service.createCalls != 0 {
		t.Errorf("Create calls = %d, want 0", service.createCalls)
	}
}

func TestClientHandlerCreatesClientAfterCsrfValidation(t *testing.T) {
	service := &clientUseCasesStub{}
	auth := clientTestAuth(nil)
	cfg := config.AuthConfig{
		SessionCookieName: "tech-service-session",
		CSRFCookieName:    "tech-service-csrf",
		TrustedOrigins:    []string{"http://localhost:3000"},
	}
	handler := NewClientHandler(auth, service, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), &fakeIdempotencyRepository{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{"phone":"555-0100"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("X-CSRF-Token", "csrf")
	request.Header.Set("Idempotency-Key", "test-client-key")
	request.AddCookie(&http.Cookie{Name: "tech-service-session", Value: "session"})
	request.AddCookie(&http.Cookie{Name: "tech-service-csrf", Value: "csrf"})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if service.createCalls != 1 {
		t.Errorf("Create calls = %d, want 1", service.createCalls)
	}
}
