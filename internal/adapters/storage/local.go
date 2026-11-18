package storageadapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrObjectNotFound      = errors.New("object not found")
	ErrObjectAlreadyExists = errors.New("object already exists")
	ErrInvalidBucket       = errors.New("invalid bucket")
)

// LocalAdapter implementa ObjectStorage usando el filesystem local.
type LocalAdapter struct {
	baseDir string
	logger  *slog.Logger
}

func NewLocalAdapter(baseDir string, logger *slog.Logger) *LocalAdapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &LocalAdapter{baseDir: baseDir, logger: logger}
}

func (a *LocalAdapter) objectPath(bucket, key string) string {
	return filepath.Join(a.baseDir, bucket, key)
}

func (a *LocalAdapter) CreateUploadURL(ctx context.Context, spec outbound.UploadSpec) (outbound.PresignedRequest, error) {
	if !domain.IsAllowedMIME(spec.ContentType) {
		return outbound.PresignedRequest{}, domain.ErrInvalidContentType
	}
	if spec.ExpectedSizeBytes <= 0 {
		return outbound.PresignedRequest{}, domain.ErrInvalidSizeBytes
	}

	// Para local, la URL apunta al endpoint local de upload del servidor.
	u := url.URL{
		Scheme: "http",
		Host:   "localhost",
		Path:   "/api/v1/storage/local/upload",
	}
	q := u.Query()
	q.Set("bucket", spec.Bucket)
	q.Set("key", spec.Key)
	q.Set("attemptId", spec.UploadAttemptID)
	u.RawQuery = q.Encode()

	headers := map[string]string{
		"Content-Type":         spec.ContentType,
		"If-None-Match":        "*",
		"x-amz-meta-upload-id": spec.UploadAttemptID,
	}

	return outbound.PresignedRequest{
		URL:       u.String(),
		Method:    http.MethodPut,
		Headers:   headers,
		ExpiresAt: time.Now().Add(spec.ExpiresIn),
	}, nil
}

func (a *LocalAdapter) CreateDownloadURL(ctx context.Context, spec outbound.DownloadSpec) (outbound.PresignedRequest, error) {
	u := url.URL{
		Scheme: "http",
		Host:   "localhost",
		Path:   "/api/v1/storage/local/download",
	}
	q := u.Query()
	q.Set("bucket", spec.Bucket)
	q.Set("key", spec.Key)
	if spec.Filename != "" {
		q.Set("filename", spec.Filename)
	}
	u.RawQuery = q.Encode()

	return outbound.PresignedRequest{
		URL:       u.String(),
		Method:    http.MethodGet,
		Headers:   map[string]string{},
		ExpiresAt: time.Now().Add(spec.ExpiresIn),
	}, nil
}

func (a *LocalAdapter) HeadObject(ctx context.Context, ref outbound.ObjectRef) (outbound.ObjectInfo, error) {
	path := a.objectPath(ref.Bucket, ref.Key)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return outbound.ObjectInfo{}, ErrObjectNotFound
		}
		return outbound.ObjectInfo{}, fmt.Errorf("stat object: %w", err)
	}

	contentType := ""
	if ext := filepath.Ext(info.Name()); ext != "" {
		contentType = mime.TypeByExtension(ext)
	}

	return outbound.ObjectInfo{
		Bucket:       ref.Bucket,
		Key:          ref.Key,
		SizeBytes:    info.Size(),
		ContentType:  contentType,
		LastModified: info.ModTime(),
		Metadata:     map[string]string{},
	}, nil
}

func (a *LocalAdapter) DeleteObject(ctx context.Context, ref outbound.ObjectRef) error {
	path := a.objectPath(ref.Bucket, ref.Key)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrObjectNotFound
		}
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// StoreFile guarda el contenido de un reader en el filesystem local bajo bucket/key.
func (a *LocalAdapter) StoreFile(bucket, key string, content io.Reader, expectedSize int64) (int64, error) {
	if err := a.ensureBucket(bucket); err != nil {
		return 0, err
	}
	path := a.objectPath(bucket, key)
	if _, err := os.Stat(path); err == nil {
		return 0, ErrObjectAlreadyExists
	}

	file, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer file.Close()

	written, err := io.Copy(file, content)
	if err != nil {
		os.Remove(path)
		return 0, fmt.Errorf("write file: %w", err)
	}
	if expectedSize > 0 && written != expectedSize {
		os.Remove(path)
		return 0, fmt.Errorf("size mismatch: expected %d, wrote %d", expectedSize, written)
	}
	return written, nil
}

// ReadFile devuelve un reader para leer el contenido del archivo local.
func (a *LocalAdapter) ReadFile(bucket, key string) (io.ReadCloser, error) {
	path := a.objectPath(bucket, key)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrObjectNotFound
		}
		return nil, fmt.Errorf("open file: %w", err)
	}
	return file, nil
}

func (a *LocalAdapter) ensureBucket(bucket string) error {
	dir := filepath.Join(a.baseDir, bucket)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create bucket directory: %w", err)
	}
	return nil
}

// ...existing code...
