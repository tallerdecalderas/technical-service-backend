package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/config"
)

func NewPool(cfg config.Config) (*pgxpool.Pool, error) {
	if cfg.DatabaseRuntimeURL == "" {
		return nil, errors.New("DATABASE_RUNTIME_URL is required")
	}
	if cfg.DatabaseMaxConns < 1 || cfg.DatabaseMinConns < 0 || cfg.DatabaseMinConns > cfg.DatabaseMaxConns {
		return nil, errors.New("invalid PostgreSQL connection pool limits")
	}

	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseRuntimeURL)
	if err != nil {
		return nil, errors.New("invalid DATABASE_RUNTIME_URL")
	}
	poolConfig.MaxConns = cfg.DatabaseMaxConns
	poolConfig.MinConns = cfg.DatabaseMinConns
	poolConfig.MaxConnLifetime = cfg.DatabaseMaxConnLifetime
	poolConfig.MaxConnIdleTime = cfg.DatabaseMaxConnIdleTime
	poolConfig.ConnConfig.ConnectTimeout = cfg.DatabaseConnectTimeout

	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, errors.New("failed to create PostgreSQL connection pool")
	}
	return pool, nil
}
