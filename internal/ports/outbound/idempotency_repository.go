package outbound

import "context"

type IdempotencyKey struct {
	UserID      string
	Operation   string
	Key         string
	RequestHash string
}

type IdempotencyRecord struct {
	RequestHash string
	StatusCode  int
	ContentType string
	Response    []byte
	Completed   bool
}

type IdempotencyRepository interface {
	Claim(context.Context, IdempotencyKey) (IdempotencyRecord, bool, error)
	Complete(context.Context, IdempotencyKey, int, string, []byte) error
	Release(context.Context, IdempotencyKey) error
}
