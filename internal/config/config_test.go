package config

import (
	"testing"
	"time"
)

func TestLoadRequiresRuntimeDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_RUNTIME_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want missing DATABASE_RUNTIME_URL error")
	}
}

func TestLoadParsesBackendSettings(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("API_PORT", "9090")
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://runtime:secret@db.example.com:5432/postgres?sslmode=verify-full")
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "12")
	t.Setenv("DATABASE_MIN_CONNS", "2")
	t.Setenv("DATABASE_MAX_CONN_LIFETIME", "45m")
	t.Setenv("DATABASE_MAX_CONN_IDLE_TIME", "4m")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "7s")
	t.Setenv("DATABASE_QUERY_TIMEOUT", "11s")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com, https://admin.example.com")
	t.Setenv("LOG_LEVEL", "warn")
	setAuthEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
	if cfg.APIPort != 9090 {
		t.Errorf("APIPort = %d, want 9090", cfg.APIPort)
	}
	if cfg.DatabaseRuntimeURL == "" {
		t.Error("DatabaseRuntimeURL is empty")
	}
	if cfg.DatabaseMaxConns != 12 {
		t.Errorf("DatabaseMaxConns = %d, want 12", cfg.DatabaseMaxConns)
	}
	if cfg.DatabaseMinConns != 2 {
		t.Errorf("DatabaseMinConns = %d, want 2", cfg.DatabaseMinConns)
	}
	if cfg.DatabaseMaxConnLifetime != 45*time.Minute {
		t.Errorf("DatabaseMaxConnLifetime = %s, want 45m", cfg.DatabaseMaxConnLifetime)
	}
	if cfg.DatabaseMaxConnIdleTime != 4*time.Minute {
		t.Errorf("DatabaseMaxConnIdleTime = %s, want 4m", cfg.DatabaseMaxConnIdleTime)
	}
	if cfg.DatabaseConnectTimeout != 7*time.Second {
		t.Errorf("DatabaseConnectTimeout = %s, want 7s", cfg.DatabaseConnectTimeout)
	}
	if cfg.DatabaseQueryTimeout != 11*time.Second {
		t.Errorf("DatabaseQueryTimeout = %s, want 11s", cfg.DatabaseQueryTimeout)
	}
	if len(cfg.CORSAllowedOrigins) != 2 || cfg.CORSAllowedOrigins[1] != "https://admin.example.com" {
		t.Errorf("CORSAllowedOrigins = %#v, want both configured origins", cfg.CORSAllowedOrigins)
	}
	if cfg.LogLevel.String() != "WARN" {
		t.Errorf("LogLevel = %s, want WARN", cfg.LogLevel)
	}
	if cfg.Auth.SupabaseURL != "https://project-ref.supabase.co" {
		t.Errorf("Auth.SupabaseURL = %q", cfg.Auth.SupabaseURL)
	}
	if cfg.Auth.PasswordResetRedirectURL != "https://app.example.com/reset-password" {
		t.Errorf("Auth.PasswordResetRedirectURL = %q", cfg.Auth.PasswordResetRedirectURL)
	}
	if cfg.Auth.SessionTTL != 168*time.Hour {
		t.Errorf("Auth.SessionTTL = %s, want 168h", cfg.Auth.SessionTTL)
	}
	if !cfg.Auth.SecureCookies {
		t.Error("Auth.SecureCookies = false in production")
	}
	if len(cfg.Auth.TrustedOrigins) != 2 {
		t.Errorf("Auth.TrustedOrigins = %#v, want both CORS origins", cfg.Auth.TrustedOrigins)
	}
}

func TestLoadRejectsInvalidPort(t *testing.T) {
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://runtime:secret@db.example.com:5432/postgres")
	t.Setenv("API_PORT", "70000")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want invalid API_PORT error")
	}
}

func TestLoadRejectsIdleConnectionsAboveMaximum(t *testing.T) {
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://runtime:secret@db.example.com:5432/postgres")
	t.Setenv("DATABASE_MAX_OPEN_CONNS", "2")
	t.Setenv("DATABASE_MIN_CONNS", "3")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want min connections validation error")
	}
}

func TestLoadRejectsHTTPOriginsInProduction(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://runtime:secret@db.example.com:5432/postgres?sslmode=verify-full")
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://app.example.com")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want production CORS scheme validation error")
	}
}

func TestLoadRequiresVerifiedTLSOutsideDevelopmentAndTests(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://runtime:secret@db.example.com:5432/postgres?sslmode=require")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want verified TLS validation error")
	}
}

func TestLoadRequiresSupabaseAuthConfiguration(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "project URL", key: "SUPABASE_URL"},
		{name: "Auth API key", key: "SUPABASE_AUTH_API_KEY"},
		{name: "JWT verification secret", key: "SUPABASE_JWT_SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_RUNTIME_URL", "postgres://user:pass@db.example.com:5432/postgres")
			setAuthEnvironment(t)
			t.Setenv(tt.key, "")
			if _, err := Load(); err == nil {
				t.Fatalf("Load() error = nil, want missing %s error", tt.key)
			}
		})
	}
}

func TestLoadAllowsLocalHTTPAndRequiresSecureAuthOriginsOutsideDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://user:pass@127.0.0.1:5432/postgres")
	setAuthEnvironment(t)
	t.Setenv("SUPABASE_URL", "http://127.0.0.1:54321")
	t.Setenv("SUPABASE_PASSWORD_RESET_REDIRECT_URL", "http://localhost:3000/reset-password")
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://localhost:3000")
	t.Setenv("CSRF_TRUSTED_ORIGINS", "http://localhost:3000")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() local HTTP error = %v", err)
	}
	if cfg.Auth.SecureCookies {
		t.Error("SecureCookies = true in development")
	}

	t.Setenv("APP_ENV", "staging")
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://user:pass@db.example.com:5432/postgres?sslmode=verify-full")
	t.Setenv("SUPABASE_PASSWORD_RESET_REDIRECT_URL", "https://app.example.com/reset-password")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://app.example.com")
	t.Setenv("CSRF_TRUSTED_ORIGINS", "http://app.example.com")
	if _, err := Load(); err == nil {
		t.Fatal("Load() staging error = nil, want HTTP trusted origin rejection")
	}
}

func TestLoadRejectsShortJWTSecret(t *testing.T) {
	t.Setenv("DATABASE_RUNTIME_URL", "postgres://user:pass@db.example.com:5432/postgres")
	setAuthEnvironment(t)
	t.Setenv("SUPABASE_JWT_SECRET", "too-short")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want short JWT secret rejection")
	}
}

func setAuthEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("SUPABASE_URL", "https://project-ref.supabase.co")
	t.Setenv("SUPABASE_AUTH_API_KEY", "publishable-test-key")
	t.Setenv("SUPABASE_JWT_SECRET", "test-jwt-secret-with-at-least-32-characters")
	t.Setenv("SUPABASE_PASSWORD_RESET_REDIRECT_URL", "https://app.example.com/reset-password")
	t.Setenv("SESSION_TTL", "168h")
}
