package supabase

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"technical-service-backend/internal/config"
	"technical-service-backend/internal/ports/outbound"
)

const testJWTSecret = "a-test-secret-long-enough-for-hmac-sha256-signing"

func TestAuthenticateUsesSupabasePasswordGrantAndReturnsTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/auth/v1/token" || r.URL.Query().Get("grant_type") != "password" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get("apikey"); got != "publishable-test-key" {
			t.Errorf("apikey = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["email"] != "user@example.com" || body["password"] != "password" {
			t.Errorf("request body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":3600}`))
	}))
	defer server.Close()

	provider := testProvider(server.URL, server.Client())
	tokens, err := provider.Authenticate(context.Background(), "user@example.com", "password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if tokens.AccessToken != "access" || tokens.RefreshToken != "refresh" || tokens.ExpiresAt.IsZero() {
		t.Errorf("tokens = %#v", tokens)
	}
}

func TestAuthenticateMapsInvalidCredentialsWithoutProviderDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"msg":"invalid login credentials"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := testProvider(server.URL, server.Client()).Authenticate(context.Background(), "user@example.com", "wrong")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Authenticate() error = %v, want ErrInvalidCredentials", err)
	}
	if strings.Contains(err.Error(), "user@example.com") || strings.Contains(err.Error(), "invalid login") {
		t.Errorf("error leaks identity/provider details: %v", err)
	}
}

func TestAuthOperationsPreserveRateLimitResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	provider := testProvider(server.URL, server.Client())
	operations := map[string]func() error{
		"login": func() error {
			_, err := provider.Authenticate(context.Background(), "user@example.com", "password")
			return err
		},
		"password recovery": func() error {
			return provider.RequestPasswordReset(context.Background(), "user@example.com")
		},
		"password reset": func() error {
			return provider.ResetPassword(context.Background(), "recovery-token", "new-password")
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, outbound.ErrRateLimited) {
				t.Fatalf("operation error = %v, want ErrRateLimited", err)
			}
		})
	}
}

func TestRequestPasswordResetDoesNotIncludeEmailInErrors(t *testing.T) {
	var requestBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/recover" || r.Method != http.MethodPost ||
			r.URL.Query().Get("redirect_to") != "http://localhost:3000/reset-password" {
			t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := testProvider(server.URL, server.Client()).RequestPasswordReset(context.Background(), "user@example.com"); err != nil {
		t.Fatalf("RequestPasswordReset() error = %v", err)
	}
	if requestBody["email"] != "user@example.com" {
		t.Errorf("recovery body = %#v", requestBody)
	}
}

func TestResetPasswordUsesRecoveryBearerToken(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/auth/v1/user" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		authorization = r.Header.Get("Authorization")
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["password"] != "new-password" {
			t.Errorf("reset body = %#v", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := testProvider(server.URL, server.Client()).ResetPassword(context.Background(), "recovery-token", "new-password"); err != nil {
		t.Fatalf("ResetPassword() error = %v", err)
	}
	if authorization != "Bearer recovery-token" {
		t.Errorf("Authorization = %q", authorization)
	}
}

func TestValidateAccessTokenRejectsInvalidSignatureAndClaims(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name   string
		claims map[string]any
		secret string
	}{
		{
			name:   "wrong issuer",
			claims: map[string]any{"iss": "https://evil.example/auth/v1", "aud": "authenticated", "sub": "subject", "exp": now.Add(time.Hour).Unix()},
			secret: testJWTSecret,
		},
		{
			name:   "wrong audience",
			claims: map[string]any{"iss": "https://project-ref.supabase.co/auth/v1", "aud": "anon", "sub": "subject", "exp": now.Add(time.Hour).Unix()},
			secret: testJWTSecret,
		},
		{
			name:   "expired",
			claims: map[string]any{"iss": "https://project-ref.supabase.co/auth/v1", "aud": "authenticated", "sub": "subject", "exp": now.Add(-time.Hour).Unix()},
			secret: testJWTSecret,
		},
		{
			name:   "empty subject",
			claims: map[string]any{"iss": "https://project-ref.supabase.co/auth/v1", "aud": "authenticated", "sub": "", "exp": now.Add(time.Hour).Unix()},
			secret: testJWTSecret,
		},
		{
			name:   "wrong signature",
			claims: map[string]any{"iss": "https://project-ref.supabase.co/auth/v1", "aud": "authenticated", "sub": "subject", "exp": now.Add(time.Hour).Unix()},
			secret: "a-different-secret-long-enough-for-hmac-sha256",
		},
		{
			name:   "not yet valid",
			claims: map[string]any{"iss": "https://project-ref.supabase.co/auth/v1", "aud": "authenticated", "sub": "subject", "exp": now.Add(time.Hour).Unix(), "nbf": now.Add(time.Minute).Unix()},
			secret: testJWTSecret,
		},
	}

	provider := testProvider("https://project-ref.supabase.co", http.DefaultClient)
	provider.now = func() time.Time { return now }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider.jwtSecret = []byte(tt.secret)
			token := signedToken(t, tt.claims, []byte(testJWTSecret))
			if _, err := provider.ValidateAccessToken(context.Background(), token); err == nil {
				t.Fatal("ValidateAccessToken() error = nil, want rejection")
			}
		})
	}

	token := signedToken(t, map[string]any{
		"iss":   "https://project-ref.supabase.co/auth/v1",
		"aud":   "authenticated",
		"sub":   "subject",
		"exp":   now.Add(time.Hour).Unix(),
		"email": "user@example.com",
	}, []byte(testJWTSecret))
	provider.jwtSecret = []byte(testJWTSecret)
	claims, err := provider.ValidateAccessToken(context.Background(), token)
	if err != nil {
		t.Fatalf("ValidateAccessToken() error = %v", err)
	}
	if claims.Subject != "subject" || claims.Email != "user@example.com" {
		t.Errorf("claims = %#v", claims)
	}
	wrongAlgorithm := signedTokenWithAlgorithm(t, "HS384", map[string]any{
		"iss": "https://project-ref.supabase.co/auth/v1",
		"aud": "authenticated",
		"sub": "subject",
		"exp": now.Add(time.Hour).Unix(),
	}, []byte(testJWTSecret))
	if _, err := provider.ValidateAccessToken(context.Background(), wrongAlgorithm); err == nil {
		t.Fatal("ValidateAccessToken() accepted a token signed with a disallowed algorithm")
	}
}

func testProvider(baseURL string, client *http.Client) *IdentityProvider {
	return NewIdentityProvider(config.AuthConfig{
		SupabaseURL:              baseURL,
		SupabaseAPIKey:           "publishable-test-key",
		JWTSecret:                []byte(testJWTSecret),
		PasswordResetRedirectURL: "http://localhost:3000/reset-password",
	}, client)
}

func signedToken(t *testing.T, claims map[string]any, secret []byte) string {
	t.Helper()
	return signedTokenWithAlgorithm(t, "HS256", claims, secret)
}

func signedTokenWithAlgorithm(t *testing.T, algorithm string, claims map[string]any, secret []byte) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": algorithm, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

var _ outbound.IdentityProvider = (*IdentityProvider)(nil)
