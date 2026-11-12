package supabase

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"technical-service-backend/internal/config"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrInvalidCredentials  = outbound.ErrInvalidCredentials
	ErrProviderUnavailable = outbound.ErrIdentityProviderUnavailable
	ErrInvalidAccessToken  = outbound.ErrInvalidAccessToken
)

const maxAuthResponseBytes = 1 << 20

type IdentityProvider struct {
	baseURL          string
	apiKey           string
	jwtSecret        []byte
	resetRedirectURL string
	client           *http.Client
	now              func() time.Time
}

func NewIdentityProvider(cfg config.AuthConfig, client *http.Client) *IdentityProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	var _ outbound.UserProvisioner = (*IdentityProvider)(nil)
	return &IdentityProvider{
		baseURL:          strings.TrimRight(cfg.SupabaseURL, "/"),
		apiKey:           cfg.SupabaseAPIKey,
		jwtSecret:        append([]byte(nil), cfg.JWTSecret...),
		resetRedirectURL: cfg.PasswordResetRedirectURL,
		client:           client,
		now:              time.Now,
	}
}

func (p *IdentityProvider) ProvisionUser(ctx context.Context, email, password string) (string, error) {
	payload, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return "", fmt.Errorf("encode Supabase sign-up request: %w", err)
	}
	response, err := p.do(ctx, http.MethodPost, p.baseURL+"/auth/v1/signup", payload, "")
	if err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) &&
			(statusErr.status == http.StatusConflict ||
				statusErr.status == http.StatusUnprocessableEntity && statusErr.code == "user_already_exists") {
			return "", outbound.ErrConflict
		}
		return "", providerError(err)
	}
	defer response.Body.Close()

	var result struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAuthResponseBytes)).Decode(&result); err != nil {
		return "", fmt.Errorf("%w: invalid sign-up response", ErrProviderUnavailable)
	}
	subject := result.User.ID
	if subject == "" {
		subject = result.ID
	}
	if subject == "" {
		return "", fmt.Errorf("%w: incomplete sign-up response", ErrProviderUnavailable)
	}
	return subject, nil
}

func (p *IdentityProvider) Authenticate(ctx context.Context, email, password string) (outbound.IdentityTokens, error) {
	payload, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return outbound.IdentityTokens{}, fmt.Errorf("encode Supabase sign-in request: %w", err)
	}
	endpoint := p.baseURL + "/auth/v1/token?grant_type=password"
	response, err := p.do(ctx, http.MethodPost, endpoint, payload, "")
	if err != nil {
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) &&
			(statusErr.status == http.StatusBadRequest || statusErr.status == http.StatusUnauthorized || statusErr.status == http.StatusUnprocessableEntity) {
			return outbound.IdentityTokens{}, ErrInvalidCredentials
		}
		return outbound.IdentityTokens{}, providerError(err)
	}
	defer response.Body.Close()

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAuthResponseBytes)).Decode(&result); err != nil {
		return outbound.IdentityTokens{}, fmt.Errorf("%w: invalid sign-in response", ErrProviderUnavailable)
	}
	if result.AccessToken == "" || result.RefreshToken == "" || result.ExpiresIn <= 0 {
		return outbound.IdentityTokens{}, fmt.Errorf("%w: incomplete sign-in response", ErrProviderUnavailable)
	}
	return outbound.IdentityTokens{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    p.now().Add(time.Duration(result.ExpiresIn) * time.Second),
	}, nil
}

func (p *IdentityProvider) RequestPasswordReset(ctx context.Context, email string) error {
	payload, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		return fmt.Errorf("encode Supabase recovery request: %w", err)
	}
	endpoint, err := url.Parse(p.baseURL + "/auth/v1/recover")
	if err != nil {
		return fmt.Errorf("build Supabase recovery URL: %w", err)
	}
	query := endpoint.Query()
	if p.resetRedirectURL != "" {
		query.Set("redirect_to", p.resetRedirectURL)
	}
	endpoint.RawQuery = query.Encode()
	response, err := p.do(ctx, http.MethodPost, endpoint.String(), payload, "")
	if err != nil {
		return providerError(err)
	}
	return closeResponse(response)
}

func (p *IdentityProvider) ResetPassword(ctx context.Context, accessToken, newPassword string) error {
	payload, err := json.Marshal(map[string]string{"password": newPassword})
	if err != nil {
		return fmt.Errorf("encode Supabase password reset request: %w", err)
	}
	response, err := p.do(ctx, http.MethodPut, p.baseURL+"/auth/v1/user", payload, accessToken)
	if err != nil {
		return providerError(err)
	}
	return closeResponse(response)
}

func (p *IdentityProvider) ValidateAccessToken(_ context.Context, accessToken string) (outbound.IdentityClaims, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 || len(p.jwtSecret) < 32 {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil || header.Algorithm != "HS256" {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}
	mac := hmac.New(sha256.New, p.jwtSecret)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}
	var claims struct {
		Issuer    string          `json:"iss"`
		Audience  json.RawMessage `json:"aud"`
		Subject   string          `json:"sub"`
		Email     string          `json:"email"`
		Expires   int64           `json:"exp"`
		NotBefore int64           `json:"nbf"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}
	audiences, ok := decodeAudiences(claims.Audience)
	if !ok || claims.Issuer != p.baseURL+"/auth/v1" || claims.Subject == "" ||
		!contains(audiences, "authenticated") || claims.Expires <= p.now().Unix() ||
		(claims.NotBefore != 0 && claims.NotBefore > p.now().Unix()) {
		return outbound.IdentityClaims{}, ErrInvalidAccessToken
	}

	return outbound.IdentityClaims{
		Subject:   claims.Subject,
		Issuer:    claims.Issuer,
		Audience:  "authenticated",
		Email:     claims.Email,
		ExpiresAt: time.Unix(claims.Expires, 0).UTC(),
	}, nil
}

func (p *IdentityProvider) do(ctx context.Context, method, endpoint string, payload []byte, accessToken string) (*http.Response, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid Supabase Auth endpoint")
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create Supabase Auth request: %w", err)
	}
	request.Header.Set("apikey", p.apiKey)
	request.Header.Set("Accept", "application/json")
	if len(payload) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send Supabase Auth request: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxAuthResponseBytes))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("%w: failed to read Auth error response", ErrProviderUnavailable)
		}
		var errorBody struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body, &errorBody)
		return nil, &httpStatusError{status: response.StatusCode, code: errorBody.Code}
	}
	return response, nil
}

func closeResponse(response *http.Response) error {
	defer response.Body.Close()
	_, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxAuthResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: failed to read Auth response", ErrProviderUnavailable)
	}
	return nil
}

func providerError(err error) error {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.status == http.StatusTooManyRequests {
		return outbound.ErrRateLimited
	}
	return fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
}

type httpStatusError struct {
	status int
	code   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("Supabase Auth returned HTTP %d", e.status)
}

func decodeAudiences(raw json.RawMessage) ([]string, bool) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, true
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err == nil {
		return multiple, true
	}
	return nil, false
}

func contains(values []string, value string) bool {
	for _, current := range values {
		if current == value {
			return true
		}
	}
	return false
}

var _ outbound.IdentityProvider = (*IdentityProvider)(nil)
