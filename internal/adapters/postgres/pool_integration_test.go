package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"technical-service-backend/internal/config"
)

func TestPoolCanPingIsolatedPostgres(t *testing.T) {
	if os.Getenv("TEST_DATABASE_ISOLATED") != "true" {
		t.Skip("set TEST_DATABASE_ISOLATED=true and TEST_DATABASE_URL to run database integration test")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required when TEST_DATABASE_ISOLATED=true")
	}

	pool, err := NewPool(config.Config{
		DatabaseRuntimeURL:      databaseURL,
		DatabaseMaxConns:        1,
		DatabaseMinConns:        0,
		DatabaseMaxConnLifetime: 30 * time.Minute,
		DatabaseMaxConnIdleTime: 5 * time.Minute,
		DatabaseConnectTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping() isolated database error = %v", err)
	}
}
