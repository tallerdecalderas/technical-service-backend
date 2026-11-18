package outbound

import (
	"context"
	"time"

	"technical-service-backend/internal/domain"
)

// StoredObjectRepository define las operaciones de persistencia sobre la tabla stored_objects en PostgreSQL.
type StoredObjectRepository interface {
	Create(ctx context.Context, obj domain.StoredObject) (domain.StoredObject, error)
	GetByID(ctx context.Context, id string, companyID string) (domain.StoredObject, error)
	UpdateStatus(ctx context.Context, id string, status string, sizeBytes *int64, confirmedAt *time.Time) error
	MarkDeletePending(ctx context.Context, id string, companyID string) error
	MarkDeleted(ctx context.Context, id string, deletedAt time.Time) error
	ListByEntity(ctx context.Context, companyID string, entityType string, entityID string) ([]domain.StoredObject, error)
	ListExpiredPending(ctx context.Context, cutoff time.Time, limit int) ([]domain.StoredObject, error)
	ListDeletePending(ctx context.Context, cutoff time.Time, limit int) ([]domain.StoredObject, error)
}
