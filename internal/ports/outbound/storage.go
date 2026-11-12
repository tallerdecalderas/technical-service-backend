package outbound

import (
	"context"
	"time"
)

type ObjectStorage interface {
	CreateUploadURL(ctx context.Context, objectKey string, expiresIn time.Duration) (SignedObjectURL, error)
	CreateDownloadURL(ctx context.Context, objectKey string, expiresIn time.Duration) (SignedObjectURL, error)
	Delete(ctx context.Context, objectKey string) error
}

type SignedObjectURL struct {
	URL       string
	ExpiresAt time.Time
}
