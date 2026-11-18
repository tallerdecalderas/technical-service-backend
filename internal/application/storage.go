package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrStorageForbidden    = errors.New("storage operation forbidden")
	ErrInvalidStorageInput = errors.New("invalid storage input")
	ErrStorageNotFound     = outbound.ErrRecordNotFound
	ErrStorageConflict     = outbound.ErrConflict
)

const (
	storageUploadURLTTL   = 10 * time.Minute
	storageDownloadURLTTL = 15 * time.Minute
)

type StorageUseCases struct {
	storage    outbound.ObjectStorage
	repository outbound.StoredObjectRepository
	logger     *slog.Logger
}

func NewStorageUseCases(storage outbound.ObjectStorage, repository outbound.StoredObjectRepository, logger *slog.Logger) *StorageUseCases {
	if logger == nil {
		logger = slog.Default()
	}
	return &StorageUseCases{storage: storage, repository: repository, logger: logger}
}

type StorageCreateUploadInput struct {
	CompanyID         string
	EntityType        string
	EntityID          string
	Category          string
	OriginalFilename  string
	ContentType       string
	ExpectedSizeBytes int64
	UploadedByID      string
}

func (s *StorageUseCases) CreateUploadURL(ctx context.Context, user domain.User, input StorageCreateUploadInput) (domain.StoredObject, string, error) {
	if !userHasCompany(user, input.CompanyID) {
		return domain.StoredObject{}, "", ErrStorageForbidden
	}

	if !domain.IsValidCategory(input.Category) {
		return domain.StoredObject{}, "", ErrInvalidStorageInput
	}
	if !domain.IsAllowedMIME(input.ContentType) {
		return domain.StoredObject{}, "", domain.ErrInvalidContentType
	}
	if input.ExpectedSizeBytes <= 0 {
		return domain.StoredObject{}, "", domain.ErrInvalidSizeBytes
	}
	if strings.TrimSpace(input.OriginalFilename) == "" {
		return domain.StoredObject{}, "", ErrInvalidStorageInput
	}

	objectID := newUUID()
	uploadAttemptID := newUUID()
	bucket := "default"
	objectKey := fmt.Sprintf("%s/%s/%s/%s", input.CompanyID, input.EntityType, input.EntityID, objectID)

	obj := domain.StoredObject{
		ID:                 objectID,
		CompanyID:          input.CompanyID,
		StorageProvider:    domain.StorageProviderLocal,
		BucketName:         bucket,
		ObjectKey:          objectKey,
		EntityType:         input.EntityType,
		EntityID:           input.EntityID,
		Category:           input.Category,
		OriginalFilename:   input.OriginalFilename,
		ContentType:        input.ContentType,
		ExpectedSizeBytes:  input.ExpectedSizeBytes,
		UploadedByID:       input.UploadedByID,
		Status:             domain.StoredObjectStatusPending,
		UploadURLExpiresAt: time.Now().Add(storageUploadURLTTL),
		UploadAttemptID:    uploadAttemptID,
		CreatedAt:          time.Now(),
	}

	created, err := s.repository.Create(ctx, obj)
	if err != nil {
		return domain.StoredObject{}, "", fmt.Errorf("create stored object record: %w", err)
	}

	spec := outbound.UploadSpec{
		Bucket:            created.BucketName,
		Key:               created.ObjectKey,
		ContentType:       created.ContentType,
		ExpectedSizeBytes: created.ExpectedSizeBytes,
		UploadAttemptID:   created.UploadAttemptID,
		ExpiresIn:         storageUploadURLTTL,
	}

	presigned, err := s.storage.CreateUploadURL(ctx, spec)
	if err != nil {
		_ = s.repository.UpdateStatus(ctx, created.ID, domain.StoredObjectStatusFailed, nil, nil)
		return domain.StoredObject{}, "", fmt.Errorf("create upload URL: %w", err)
	}

	return created, presigned.URL, nil
}

type StorageConfirmInput struct {
	ObjectID  string
	CompanyID string
	SizeBytes int64
	Checksum  string
}

func (s *StorageUseCases) ConfirmUpload(ctx context.Context, user domain.User, input StorageConfirmInput) (domain.StoredObject, error) {
	if !userHasCompany(user, input.CompanyID) {
		return domain.StoredObject{}, ErrStorageForbidden
	}

	obj, err := s.repository.GetByID(ctx, input.ObjectID, input.CompanyID)
	if err != nil {
		return domain.StoredObject{}, err
	}

	if obj.Status != domain.StoredObjectStatusPending {
		return domain.StoredObject{}, ErrStorageConflict
	}

	ref := outbound.ObjectRef{Bucket: obj.BucketName, Key: obj.ObjectKey}
	info, err := s.storage.HeadObject(ctx, ref)
	if err != nil {
		if errors.Is(err, outbound.ErrRecordNotFound) || strings.Contains(err.Error(), "not found") {
			_ = s.repository.UpdateStatus(ctx, obj.ID, domain.StoredObjectStatusFailed, nil, nil)
			return domain.StoredObject{}, ErrStorageNotFound
		}
		return domain.StoredObject{}, fmt.Errorf("head object: %w", err)
	}

	if info.SizeBytes != input.SizeBytes {
		_ = s.repository.UpdateStatus(ctx, obj.ID, domain.StoredObjectStatusFailed, nil, nil)
		return domain.StoredObject{}, ErrInvalidStorageInput
	}

	now := time.Now()
	if err := s.repository.UpdateStatus(ctx, obj.ID, domain.StoredObjectStatusAvailable, &input.SizeBytes, &now); err != nil {
		return domain.StoredObject{}, fmt.Errorf("confirm upload: %w", err)
	}

	obj.Status = domain.StoredObjectStatusAvailable
	obj.SizeBytes = &input.SizeBytes
	obj.ChecksumSHA256 = &input.Checksum
	obj.ConfirmedAt = &now
	return obj, nil
}

type StorageDownloadURLInput struct {
	ObjectID  string
	CompanyID string
	Filename  string
}

func (s *StorageUseCases) GetDownloadURL(ctx context.Context, user domain.User, input StorageDownloadURLInput) (string, error) {
	if !userHasCompany(user, input.CompanyID) {
		return "", ErrStorageForbidden
	}

	obj, err := s.repository.GetByID(ctx, input.ObjectID, input.CompanyID)
	if err != nil {
		return "", err
	}

	if obj.Status != domain.StoredObjectStatusAvailable {
		return "", ErrStorageNotFound
	}

	spec := outbound.DownloadSpec{
		Bucket:    obj.BucketName,
		Key:       obj.ObjectKey,
		Filename:  input.Filename,
		ExpiresIn: storageDownloadURLTTL,
	}

	presigned, err := s.storage.CreateDownloadURL(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("create download URL: %w", err)
	}
	return presigned.URL, nil
}

type StorageDeleteInput struct {
	ObjectID  string
	CompanyID string
}

func (s *StorageUseCases) DeleteObject(ctx context.Context, user domain.User, input StorageDeleteInput) error {
	if !userHasCompany(user, input.CompanyID) {
		return ErrStorageForbidden
	}

	obj, err := s.repository.GetByID(ctx, input.ObjectID, input.CompanyID)
	if err != nil {
		return err
	}

	if obj.Status == domain.StoredObjectStatusDeleted {
		return ErrStorageNotFound
	}

	if err := s.repository.MarkDeletePending(ctx, input.ObjectID, input.CompanyID); err != nil {
		return fmt.Errorf("mark delete pending: %w", err)
	}

	if err := s.storage.DeleteObject(ctx, outbound.ObjectRef{Bucket: obj.BucketName, Key: obj.ObjectKey}); err != nil {
		s.logger.Warn("failed to delete object from storage", "object_id", obj.ID, "error", err)
		return nil
	}

	if err := s.repository.MarkDeleted(ctx, input.ObjectID, time.Now()); err != nil {
		return fmt.Errorf("mark deleted: %w", err)
	}
	return nil
}

func (s *StorageUseCases) ListByEntity(ctx context.Context, user domain.User, companyID, entityType, entityID string) ([]domain.StoredObject, error) {
	if !userHasCompany(user, companyID) {
		return nil, ErrStorageForbidden
	}
	return s.repository.ListByEntity(ctx, companyID, entityType, entityID)
}

func (s *StorageUseCases) ReconcileExpiredPending(ctx context.Context, before time.Time, limit int) ([]domain.StoredObject, error) {
	objects, err := s.repository.ListExpiredPending(ctx, before, limit)
	if err != nil {
		return nil, err
	}
	for _, obj := range objects {
		if _, err := s.storage.HeadObject(ctx, outbound.ObjectRef{Bucket: obj.BucketName, Key: obj.ObjectKey}); err == nil {
			_ = s.repository.UpdateStatus(ctx, obj.ID, domain.StoredObjectStatusAvailable, nil, nil)
			continue
		}
		_ = s.repository.UpdateStatus(ctx, obj.ID, domain.StoredObjectStatusFailed, nil, nil)
	}
	return objects, nil
}

func (s *StorageUseCases) ProcessDeletePending(ctx context.Context, before time.Time, limit int) ([]domain.StoredObject, error) {
	objects, err := s.repository.ListDeletePending(ctx, before, limit)
	if err != nil {
		return nil, err
	}
	for _, obj := range objects {
		if err := s.storage.DeleteObject(ctx, outbound.ObjectRef{Bucket: obj.BucketName, Key: obj.ObjectKey}); err != nil {
			s.logger.Warn("failed physical delete", "object_id", obj.ID, "error", err)
			continue
		}
		_ = s.repository.MarkDeleted(ctx, obj.ID, time.Now())
	}
	return objects, nil
}

func userHasCompany(user domain.User, companyID string) bool {
	if user.ID == "" {
		return false
	}
	for _, cid := range user.CompanyIDs {
		if cid == companyID {
			return true
		}
	}
	return false
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], b[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], b[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], b[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], b[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], b[10:16])
	return string(encoded[:])
}
