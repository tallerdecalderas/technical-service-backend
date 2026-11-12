package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func rollback(tx pgx.Tx) {
	_ = tx.Rollback(context.Background())
}
