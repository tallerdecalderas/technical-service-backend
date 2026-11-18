package outbound

import (
	"context"
	"io"
	"time"
)

// ObjectStorage define las capacidades de almacenamiento de objetos
// desacopladas de cualquier SDK o proveedor (Cloudflare R2, MinIO, filesystem).
type ObjectStorage interface {
	CreateUploadURL(ctx context.Context, input UploadSpec) (PresignedRequest, error)
	CreateDownloadURL(ctx context.Context, input DownloadSpec) (PresignedRequest, error)
	HeadObject(ctx context.Context, ref ObjectRef) (ObjectInfo, error)
	DeleteObject(ctx context.Context, ref ObjectRef) error
}

// ObjectRef identifica unívocamente un objeto por bucket y key interna.
type ObjectRef struct {
	Bucket string
	Key    string
}

// UploadSpec contiene los datos necesarios para generar una URL prefirmada de carga directa.
type UploadSpec struct {
	Bucket            string
	Key               string
	ContentType       string
	ExpectedSizeBytes int64
	UploadAttemptID   string
	ExpiresIn         time.Duration
}

// DownloadSpec contiene los datos necesarios para generar una URL prefirmada de descarga.
type DownloadSpec struct {
	Bucket    string
	Key       string
	ExpiresIn time.Duration
	Filename  string
}

// PresignedRequest representa la petición HTTP que el cliente debe ejecutar directamente contra el almacenamiento.
type PresignedRequest struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// ObjectInfo contiene los metadatos devueltos por HeadObject al inspeccionar el objeto remoto o local.
type ObjectInfo struct {
	Bucket       string
	Key          string
	SizeBytes    int64
	ContentType  string
	UploadID     string
	Metadata     map[string]string
	ETag         string
	LastModified time.Time
}

// FileStore expone operaciones directas de lectura/escritura de archivos
// para el adaptador de filesystem local (usado en pruebas sin R2).
type FileStore interface {
	StoreFile(bucket, key string, content io.Reader, expectedSize int64) (int64, error)
	ReadFile(bucket, key string) (io.ReadCloser, error)
}
