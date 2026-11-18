package storageadapter

import (
	"io"
)

// FileStore expone operaciones directas de lectura/escritura de archivos
// para el adaptador de filesystem local (usado en pruebas sin R2).
type FileStore interface {
	StoreFile(bucket, key string, content io.Reader, expectedSize int64) (int64, error)
	ReadFile(bucket, key string) (io.ReadCloser, error)
}
