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
)

type healthCheckerFunc func(context.Context) error

func (f healthCheckerFunc) Ping(ctx context.Context) error {
	return f(ctx)
}

func TestHealthEndpointsReportProcessAndDatabaseHealth(t *testing.T) {
	handler := NewHandler(
		healthCheckerFunc(func(context.Context) error { return nil }),
		[]string{"http://localhost:3000"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	for _, path := range []string{"/health/live", "/health/ready"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q", contentType)
			}
			if response.Header().Get("X-Request-ID") == "" {
				t.Error("X-Request-ID header is missing")
			}
			if !strings.Contains(response.Body.String(), `"status":"ok"`) {
				t.Errorf("body = %q, want healthy JSON status", response.Body.String())
			}
		})
	}
}

func TestLivenessDoesNotDependOnDatabase(t *testing.T) {
	handler := NewHandler(
		healthCheckerFunc(func(context.Context) error { return errors.New("db unavailable") }),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestReadinessReturnsServiceUnavailableWhenDatabaseIsDown(t *testing.T) {
	handler := NewHandler(
		healthCheckerFunc(func(context.Context) error { return errors.New("db unavailable") }),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if strings.Contains(response.Body.String(), "db unavailable") {
		t.Errorf("response leaked database error: %q", response.Body.String())
	}
}

func TestCORSAllowsConfiguredOriginAndRejectsOtherPreflights(t *testing.T) {
	handler := NewHandler(
		healthCheckerFunc(func(context.Context) error { return nil }),
		[]string{"https://app.example.com"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	allowed := httptest.NewRecorder()
	allowedRequest := httptest.NewRequest(http.MethodOptions, "/api/v1/services", nil)
	allowedRequest.Header.Set("Origin", "https://app.example.com")
	allowedRequest.Header.Set("Access-Control-Request-Method", "POST")
	handler.ServeHTTP(allowed, allowedRequest)

	if allowed.Code != http.StatusNoContent {
		t.Fatalf("allowed preflight status = %d, want %d", allowed.Code, http.StatusNoContent)
	}
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if allowed.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("credentialed CORS header is missing")
	}
	if allowed.Header().Get("Access-Control-Expose-Headers") != "X-Request-ID" {
		t.Error("X-Request-ID is not exposed to the configured browser origin")
	}

	rejected := httptest.NewRecorder()
	rejectedRequest := httptest.NewRequest(http.MethodOptions, "/api/v1/services", nil)
	rejectedRequest.Header.Set("Origin", "https://evil.example")
	rejectedRequest.Header.Set("Access-Control-Request-Method", "POST")
	handler.ServeHTTP(rejected, rejectedRequest)

	if rejected.Code != http.StatusForbidden {
		t.Fatalf("unconfigured preflight status = %d, want %d", rejected.Code, http.StatusForbidden)
	}
	if rejected.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("unconfigured origin received CORS permission")
	}
}

func TestCORSRejectsUnapprovedRequestHeaders(t *testing.T) {
	handler := NewHandler(
		healthCheckerFunc(func(context.Context) error { return nil }),
		[]string{"https://app.example.com"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/services", nil)
	request.Header.Set("Origin", "https://app.example.com")
	request.Header.Set("Access-Control-Request-Method", "POST")
	request.Header.Set("Access-Control-Request-Headers", "X-Admin-Override")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestNewRequestIDUsesUUIDv4Format(t *testing.T) {
	requestID, err := newRequestID()
	if err != nil {
		t.Fatalf("newRequestID() error = %v", err)
	}
	if len(requestID) != 36 || requestID[14] != '4' {
		t.Errorf("request ID = %q, want UUIDv4", requestID)
	}
}

func TestRequestLogsDoNotIncludeQueryParameters(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := NewHandler(
		healthCheckerFunc(func(context.Context) error { return nil }),
		nil,
		logger,
	)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/live?token=secret-value", nil)
	handler.ServeHTTP(response, request)

	if strings.Contains(logs.String(), "secret-value") {
		t.Errorf("request log contains query parameter: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "/health/live") {
		t.Errorf("request log does not include request path: %s", logs.String())
	}
}

func TestNewServerSetsTimeouts(t *testing.T) {
	server := NewServer(8080, http.NotFoundHandler())

	if server.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", server.Addr)
	}
	if server.ReadHeaderTimeout == 0 || server.ReadTimeout == 0 || server.WriteTimeout == 0 || server.IdleTimeout == 0 {
		t.Error("HTTP server timeouts must all be configured")
	}
}

func TestServeShutsDownWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(0, http.NotFoundHandler())
	result := make(chan error, 1)

	go func() {
		result <- Serve(ctx, server, logger)
	}()
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Serve() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return after context cancellation")
	}
}
