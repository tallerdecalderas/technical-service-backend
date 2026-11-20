package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment             string
	APIPort                 int
	DatabaseRuntimeURL      string
	DatabaseMaxConns        int32
	DatabaseMinConns        int32
	DatabaseMaxConnLifetime time.Duration
	DatabaseMaxConnIdleTime time.Duration
	DatabaseConnectTimeout  time.Duration
	DatabaseQueryTimeout    time.Duration
	CORSAllowedOrigins      []string
	Auth                    AuthConfig
	GoogleSheets            GoogleSheetsConfig
	Storage                 StorageConfig
	LogLevel                slog.Level
}

type StorageConfig struct {
	Provider      string
	LocalDir      string
	R2Endpoint    string
	R2Bucket      string
	R2AccessKeyID string
	R2SecretKey   string
	R2Region      string
}

type GoogleSheetsConfig struct {
	CredentialsJSON string
	SpreadsheetID   string
	CatalogRange    string
}

type AuthConfig struct {
	SupabaseURL              string
	SupabaseAPIKey           string
	JWTSecret                []byte
	PasswordResetRedirectURL string
	SessionCookieName        string
	CSRFCookieName           string
	SessionTTL               time.Duration
	TrustedOrigins           []string
	SecureCookies            bool
}

func Load() (Config, error) {
	loadDotEnv(".env")
	environment := envOrDefault("APP_ENV", "development")
	switch environment {
	case "development", "test", "staging", "production":
	default:
		return Config{}, fmt.Errorf("APP_ENV must be development, test, staging, or production")
	}

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_RUNTIME_URL"))
	if databaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_RUNTIME_URL is required")
	}
	if err := validatePostgresURL(databaseURL, environment == "staging" || environment == "production"); err != nil {
		return Config{}, fmt.Errorf("DATABASE_RUNTIME_URL: %w", err)
	}

	port, err := intEnv("API_PORT", 8080)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("API_PORT must be an integer between 1 and 65535")
	}

	maxConns, err := intEnv("DATABASE_MAX_OPEN_CONNS", 10)
	if err != nil || maxConns < 1 {
		return Config{}, fmt.Errorf("DATABASE_MAX_OPEN_CONNS must be a positive integer")
	}
	minConns, err := intEnv("DATABASE_MIN_CONNS", 0)
	if err != nil || minConns < 0 || minConns > maxConns {
		return Config{}, fmt.Errorf("DATABASE_MIN_CONNS must be between 0 and DATABASE_MAX_OPEN_CONNS")
	}

	maxConnLifetime, err := durationEnv("DATABASE_MAX_CONN_LIFETIME", 30*time.Minute)
	if err != nil || maxConnLifetime <= 0 {
		return Config{}, fmt.Errorf("DATABASE_MAX_CONN_LIFETIME must be a positive duration")
	}
	maxConnIdleTime, err := durationEnv("DATABASE_MAX_CONN_IDLE_TIME", 5*time.Minute)
	if err != nil || maxConnIdleTime <= 0 {
		return Config{}, fmt.Errorf("DATABASE_MAX_CONN_IDLE_TIME must be a positive duration")
	}
	connectTimeout, err := durationEnv("DATABASE_CONNECT_TIMEOUT", 5*time.Second)
	if err != nil || connectTimeout <= 0 {
		return Config{}, fmt.Errorf("DATABASE_CONNECT_TIMEOUT must be a positive duration")
	}
	queryTimeout, err := durationEnv("DATABASE_QUERY_TIMEOUT", 10*time.Second)
	if err != nil || queryTimeout <= 0 {
		return Config{}, fmt.Errorf("DATABASE_QUERY_TIMEOUT must be a positive duration")
	}

	origins, err := originsEnv(environment)
	if err != nil {
		return Config{}, err
	}

	logLevel, err := parseLogLevel(envOrDefault("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	auth, err := authConfig(environment, origins)
	if err != nil {
		return Config{}, err
	}

	storage, err := storageConfig(environment)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Environment:             environment,
		APIPort:                 port,
		DatabaseRuntimeURL:      databaseURL,
		DatabaseMaxConns:        int32(maxConns),
		DatabaseMinConns:        int32(minConns),
		DatabaseMaxConnLifetime: maxConnLifetime,
		DatabaseMaxConnIdleTime: maxConnIdleTime,
		DatabaseConnectTimeout:  connectTimeout,
		DatabaseQueryTimeout:    queryTimeout,
		CORSAllowedOrigins:      origins,
		Auth:                    auth,
		GoogleSheets: GoogleSheetsConfig{
			CredentialsJSON: strings.TrimSpace(os.Getenv("GOOGLE_SHEETS_CREDENTIALS_JSON")),
			SpreadsheetID:   strings.TrimSpace(os.Getenv("GOOGLE_SHEETS_SPREADSHEET_ID")),
			CatalogRange:    envOrDefault("GOOGLE_SHEETS_CATALOG_RANGE", "A1:Z10000"),
		},
		Storage:  storage,
		LogLevel: logLevel,
	}, nil
}

func storageConfig(environment string) (StorageConfig, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("STORAGE_PROVIDER")))
	switch provider {
	case "", "local":
		provider = "local"
	case "r2":
	default:
		return StorageConfig{}, fmt.Errorf("STORAGE_PROVIDER must be local or r2")
	}

	cfg := StorageConfig{
		Provider: provider,
		LocalDir: envOrDefault("STORAGE_LOCAL_DIR", "./uploads"),
	}

	if provider == "r2" {
		cfg.R2Endpoint = strings.TrimRight(strings.TrimSpace(os.Getenv("R2_ENDPOINT")), "/")
		cfg.R2Bucket = strings.TrimSpace(os.Getenv("R2_BUCKET"))
		cfg.R2AccessKeyID = strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID"))
		cfg.R2SecretKey = strings.TrimSpace(os.Getenv("R2_SECRET_KEY"))
		cfg.R2Region = strings.TrimSpace(os.Getenv("R2_REGION"))
		if cfg.R2Endpoint == "" || cfg.R2Bucket == "" || cfg.R2AccessKeyID == "" || cfg.R2SecretKey == "" {
			return StorageConfig{}, fmt.Errorf("R2_ENDPOINT, R2_BUCKET, R2_ACCESS_KEY_ID and R2_SECRET_KEY are required when STORAGE_PROVIDER=r2")
		}
		if cfg.R2Region == "" {
			cfg.R2Region = "auto"
		}
	}

	return cfg, nil
}

func authConfig(environment string, corsOrigins []string) (AuthConfig, error) {
	supabaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SUPABASE_URL")), "/")
	if supabaseURL == "" {
		return AuthConfig{}, fmt.Errorf("SUPABASE_URL is required")
	}
	parsedURL, err := url.Parse(supabaseURL)
	if err != nil || parsedURL.Host == "" ||
		(parsedURL.Scheme != "https" && parsedURL.Scheme != "http") ||
		parsedURL.RawQuery != "" || parsedURL.Fragment != "" || parsedURL.Path != "" {
		return AuthConfig{}, fmt.Errorf("SUPABASE_URL must be an HTTP(S) project origin")
	}
	if environment != "development" && environment != "test" && parsedURL.Scheme != "https" {
		return AuthConfig{}, fmt.Errorf("SUPABASE_URL must use HTTPS outside development and tests")
	}

	apiKey := strings.TrimSpace(os.Getenv("SUPABASE_AUTH_API_KEY"))
	if apiKey == "" {
		return AuthConfig{}, fmt.Errorf("SUPABASE_AUTH_API_KEY is required")
	}
	jwtSecret := strings.TrimSpace(os.Getenv("SUPABASE_JWT_SECRET"))
	if len(jwtSecret) < 32 {
		return AuthConfig{}, fmt.Errorf("SUPABASE_JWT_SECRET must contain at least 32 characters")
	}

	resetRedirect := strings.TrimSpace(os.Getenv("SUPABASE_PASSWORD_RESET_REDIRECT_URL"))
	if resetRedirect == "" {
		if environment == "development" || environment == "test" {
			resetRedirect = "http://localhost:3000/reset-password"
		} else {
			return AuthConfig{}, fmt.Errorf("SUPABASE_PASSWORD_RESET_REDIRECT_URL is required in staging and production")
		}
	}
	parsedRedirect, err := url.Parse(resetRedirect)
	if err != nil || parsedRedirect.Host == "" ||
		(parsedRedirect.Scheme != "https" && parsedRedirect.Scheme != "http") ||
		parsedRedirect.User != nil || parsedRedirect.Fragment != "" {
		return AuthConfig{}, fmt.Errorf("SUPABASE_PASSWORD_RESET_REDIRECT_URL must be a valid HTTP(S) URL")
	}
	if environment != "development" && environment != "test" && parsedRedirect.Scheme != "https" {
		return AuthConfig{}, fmt.Errorf("SUPABASE_PASSWORD_RESET_REDIRECT_URL must use HTTPS outside development and tests")
	}

	sessionTTL, err := durationEnv("SESSION_TTL", 168*time.Hour)
	if err != nil || sessionTTL <= 0 {
		return AuthConfig{}, fmt.Errorf("SESSION_TTL must be a positive duration")
	}
	sessionCookieName := envOrDefault("SESSION_COOKIE_NAME", "tech-service-session")
	csrfCookieName := envOrDefault("CSRF_COOKIE_NAME", "tech-service-csrf")
	if !validCookieName(sessionCookieName) || !validCookieName(csrfCookieName) ||
		sessionCookieName == csrfCookieName {
		return AuthConfig{}, fmt.Errorf("session and CSRF cookie names must be distinct valid names")
	}

	trustedOrigins, err := configuredOrigins("CSRF_TRUSTED_ORIGINS", environment, corsOrigins)
	if err != nil {
		return AuthConfig{}, err
	}
	return AuthConfig{
		SupabaseURL:              supabaseURL,
		SupabaseAPIKey:           apiKey,
		JWTSecret:                []byte(jwtSecret),
		PasswordResetRedirectURL: resetRedirect,
		SessionCookieName:        sessionCookieName,
		CSRFCookieName:           csrfCookieName,
		SessionTTL:               sessionTTL,
		TrustedOrigins:           trustedOrigins,
		SecureCookies:            environment == "staging" || environment == "production",
	}, nil
}

func validCookieName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if character < 0x21 || character > 0x7e ||
			strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", character) {
			return false
		}
	}
	return true
}

func validatePostgresURL(raw string, requireVerifiedTLS bool) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must be a valid PostgreSQL URL")
	}
	if (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
		return fmt.Errorf("must use postgres:// or postgresql:// and include a host")
	}
	if requireVerifiedTLS && parsed.Query().Get("sslmode") != "verify-full" {
		return fmt.Errorf("must set sslmode=verify-full outside development and tests")
	}
	return nil
}

func originsEnv(environment string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		if environment == "production" || environment == "staging" {
			return nil, fmt.Errorf("CORS_ALLOWED_ORIGINS is required in staging and production")
		}
		return []string{"http://localhost:3000"}, nil
	}

	return parseOrigins("CORS_ALLOWED_ORIGINS", raw, environment)
}

func configuredOrigins(key, environment string, defaults []string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return append([]string(nil), defaults...), nil
	}
	return parseOrigins(key, raw, environment)
}

func parseOrigins(key, raw, environment string) ([]string, error) {
	origins := make([]string, 0)
	for _, entry := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(entry)
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("%s must contain only HTTP(S) origins", key)
		}
		if (environment == "production" || environment == "staging") && parsed.Scheme != "https" {
			return nil, fmt.Errorf("%s must use HTTPS in staging and production", key)
		}
		if origin == "*" {
			return nil, fmt.Errorf("%s does not allow wildcard origins", key)
		}
		for _, existing := range origins {
			if existing == origin {
				return nil, fmt.Errorf("%s contains a duplicate origin", key)
			}
		}
		origins = append(origins, origin)
	}
	return origins, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return value, nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", key)
	}
	return value, nil
}

func loadDotEnv(filepath string) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if (strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"")) ||
			(strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'")) {
			if len(val) >= 2 {
				val = val[1 : len(val)-1]
			}
		}
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}
