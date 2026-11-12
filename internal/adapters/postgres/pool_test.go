package postgres

import (
	"testing"
	"time"

	"technical-service-backend/internal/config"
)

func TestNewPoolUsesConfiguredLimitsWithoutOpeningDatabaseConnection(t *testing.T) {
	cfg := config.Config{
		DatabaseRuntimeURL:      "postgres://user:password@127.0.0.1:1/postgres?sslmode=disable",
		DatabaseMaxConns:        9,
		DatabaseMinConns:        2,
		DatabaseMaxConnLifetime: 20 * time.Minute,
		DatabaseMaxConnIdleTime: 3 * time.Minute,
	}

	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Close()

	poolConfig := pool.Config()
	if poolConfig.MaxConns != 9 {
		t.Errorf("MaxConns = %d, want 9", poolConfig.MaxConns)
	}
	if poolConfig.MinConns != 2 {
		t.Errorf("MinConns = %d, want 2", poolConfig.MinConns)
	}
	if poolConfig.MaxConnLifetime != 20*time.Minute {
		t.Errorf("MaxConnLifetime = %s, want 20m", poolConfig.MaxConnLifetime)
	}
	if poolConfig.MaxConnIdleTime != 3*time.Minute {
		t.Errorf("MaxConnIdleTime = %s, want 3m", poolConfig.MaxConnIdleTime)
	}
}

func TestNewPoolRejectsInvalidDatabaseURL(t *testing.T) {
	_, err := NewPool(config.Config{DatabaseRuntimeURL: "not-a-postgres-url"})
	if err == nil {
		t.Fatal("NewPool() error = nil, want invalid URL error")
	}
}
