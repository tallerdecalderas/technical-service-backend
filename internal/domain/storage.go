package domain

import (
	"errors"
	"strings"
	"time"
)

// Estados del ciclo de vida de un objeto almacenado.
const (
	StoredObjectStatusPending       = "PENDING"
	StoredObjectStatusAvailable     = "AVAILABLE"
	StoredObjectStatusDeletePending = "DELETE_PENDING"
	StoredObjectStatusDeleted       = "DELETED"
	StoredObjectStatusFailed        = "FAILED"
)

// Categorías funcionales cerradas de objetos.
const (
	StorageCategoryPhotos    = "photos"
	StorageCategoryReceipts  = "receipts"
	StorageCategoryDocuments = "documents"
)

// Proveedores de almacenamiento soportados.
const (
	StorageProviderR2    = "r2"
	StorageProviderLocal = "local"
)

var (
	ErrInvalidCategory    = errors.New("invalid storage category")
	ErrInvalidContentType = errors.New("content type not allowed")
	ErrInvalidSizeBytes   = errors.New("invalid size bytes: must be positive")
	ErrFileSizeExceeded   = errors.New("file size exceeds maximum allowed")
)

// Allowlist oficial de tipos MIME admitidos.
var allowedMIMETypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
	"application/pdf": true,
}

// StoredObject representa un archivo binario persistido con metadatos en PostgreSQL.
type StoredObject struct {
	ID                 string     `json:"id"`
	CompanyID          string     `json:"companyId"`
	StorageProvider    string     `json:"storageProvider"`
	BucketName         string     `json:"bucketName"`
	ObjectKey          string     `json:"objectKey"`
	EntityType         string     `json:"entityType"`
	EntityID           string     `json:"entityId"`
	Category           string     `json:"category"`
	OriginalFilename   string     `json:"originalFilename"`
	ContentType        string     `json:"contentType"`
	ExpectedSizeBytes  int64      `json:"expectedSizeBytes"`
	SizeBytes          *int64     `json:"sizeBytes,omitempty"`
	ChecksumSHA256     *string    `json:"checksumSha256,omitempty"`
	UploadedByID       string     `json:"uploadedById"`
	Status             string     `json:"status"`
	UploadURLExpiresAt time.Time  `json:"uploadUrlExpiresAt"`
	UploadAttemptID    string     `json:"uploadAttemptId"`
	CreatedAt          time.Time  `json:"createdAt"`
	ConfirmedAt        *time.Time `json:"confirmedAt,omitempty"`
	DeletedAt          *time.Time `json:"deletedAt,omitempty"`
}

// IsValidCategory comprueba si la categoría pertenece a la lista cerrada.
func IsValidCategory(cat string) bool {
	switch strings.ToLower(strings.TrimSpace(cat)) {
	case StorageCategoryPhotos, StorageCategoryReceipts, StorageCategoryDocuments:
		return true
	default:
		return false
	}
}

// IsAllowedMIME comprueba si el tipo de contenido está en la lista permitida.
func IsAllowedMIME(mime string) bool {
	clean := strings.ToLower(strings.TrimSpace(mime))
	return allowedMIMETypes[clean]
}
