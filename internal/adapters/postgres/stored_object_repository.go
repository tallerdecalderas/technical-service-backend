package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type storedObjectDB interface {
	Begin(context.Context) (pgx.Tx, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type StoredObjectRepository struct {
	db           storedObjectDB
	queryTimeout time.Duration
}

func NewStoredObjectRepository(pool *pgxpool.Pool, queryTimeout time.Duration) *StoredObjectRepository {
	return &StoredObjectRepository{db: pool, queryTimeout: queryTimeout}
}

func (r *StoredObjectRepository) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.queryTimeout)
}

type StoredObjectFilter struct {
	CompanyID    string
	EntityType   string
	EntityID     string
	Category     string
	Status       string
	UploadedByID string
	Limit        int
	Offset       int
}

func (r *StoredObjectRepository) Create(ctx context.Context, obj domain.StoredObject) (domain.StoredObject, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
insert into public."storedObject"
    (id, company_id, storage_provider, bucket_name, object_key, entity_type, entity_id,
     category, original_filename, content_type, expected_size_bytes, size_bytes,
     checksum_sha256, uploaded_by_id, status, upload_url_expires_at, upload_attempt_id, created_at)
values
    ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid, $8, $9, $10, $11::bigint, $12::bigint,
     $13, $14::uuid, $15, $16::timestamptz, $17, $18::timestamptz)
returning id::text, company_id::text, storage_provider, bucket_name, object_key, entity_type,
          entity_id::text, category, original_filename, content_type, expected_size_bytes,
          size_bytes, checksum_sha256, uploaded_by_id::text, status, upload_url_expires_at,
          upload_attempt_id, created_at, confirmed_at, deleted_at
`

	var out domain.StoredObject
	var confirmedAt, deletedAt *time.Time
	var sizeBytes *int64
	var checksum *string
	var uploadAttemptID *string

	err := r.db.QueryRow(queryCtx, query,
		obj.ID, obj.CompanyID, obj.StorageProvider, obj.BucketName, obj.ObjectKey,
		obj.EntityType, obj.EntityID, obj.Category, obj.OriginalFilename, obj.ContentType,
		obj.ExpectedSizeBytes, obj.SizeBytes, obj.ChecksumSHA256, obj.UploadedByID,
		obj.Status, obj.UploadURLExpiresAt, obj.UploadAttemptID, obj.CreatedAt,
	).Scan(
		&out.ID, &out.CompanyID, &out.StorageProvider, &out.BucketName, &out.ObjectKey,
		&out.EntityType, &out.EntityID, &out.Category, &out.OriginalFilename, &out.ContentType,
		&out.ExpectedSizeBytes, &sizeBytes, &checksum, &out.UploadedByID, &out.Status,
		&out.UploadURLExpiresAt, &uploadAttemptID, &out.CreatedAt, &confirmedAt, &deletedAt,
	)
	if err != nil {
		return domain.StoredObject{}, fmt.Errorf("create stored object: %w", err)
	}
	out.SizeBytes = sizeBytes
	out.ChecksumSHA256 = checksum
	out.ConfirmedAt = confirmedAt
	out.DeletedAt = deletedAt
	if uploadAttemptID != nil {
		out.UploadAttemptID = *uploadAttemptID
	}
	return out, nil
}

func (r *StoredObjectRepository) GetByID(ctx context.Context, id, companyID string) (domain.StoredObject, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
select id::text, company_id::text, storage_provider, bucket_name, object_key, entity_type,
       entity_id::text, category, original_filename, content_type, expected_size_bytes,
       size_bytes, checksum_sha256, uploaded_by_id::text, status, upload_url_expires_at,
       upload_attempt_id, created_at, confirmed_at, deleted_at
from public."storedObject"
where id = $1::uuid and company_id = $2::uuid
`

	var out domain.StoredObject
	var confirmedAt, deletedAt *time.Time
	var sizeBytes *int64
	var checksum *string
	var uploadAttemptID *string

	err := r.db.QueryRow(queryCtx, query, id, companyID).Scan(
		&out.ID, &out.CompanyID, &out.StorageProvider, &out.BucketName, &out.ObjectKey,
		&out.EntityType, &out.EntityID, &out.Category, &out.OriginalFilename, &out.ContentType,
		&out.ExpectedSizeBytes, &sizeBytes, &checksum, &out.UploadedByID, &out.Status,
		&out.UploadURLExpiresAt, &uploadAttemptID, &out.CreatedAt, &confirmedAt, &deletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StoredObject{}, outbound.ErrRecordNotFound
		}
		return domain.StoredObject{}, fmt.Errorf("get stored object: %w", err)
	}
	out.SizeBytes = sizeBytes
	out.ChecksumSHA256 = checksum
	out.ConfirmedAt = confirmedAt
	out.DeletedAt = deletedAt
	if uploadAttemptID != nil {
		out.UploadAttemptID = *uploadAttemptID
	}
	return out, nil
}

func (r *StoredObjectRepository) MarkDeleted(ctx context.Context, id string, deletedAt time.Time) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
update public."storedObject"
set status = 'DELETED', deleted_at = $1::timestamptz
where id = $2::uuid
`

	result, err := r.db.Exec(queryCtx, query, deletedAt, id)
	if err != nil {
		return fmt.Errorf("mark deleted: %w", err)
	}
	if result.RowsAffected() == 0 {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *StoredObjectRepository) ListByEntity(ctx context.Context, companyID, entityType, entityID string) ([]domain.StoredObject, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
select id::text, company_id::text, storage_provider, bucket_name, object_key, entity_type,
       entity_id::text, category, original_filename, content_type, expected_size_bytes,
       size_bytes, checksum_sha256, uploaded_by_id::text, status, upload_url_expires_at,
       upload_attempt_id, created_at, confirmed_at, deleted_at
from public."storedObject"
where company_id = $1::uuid and entity_type = $2 and entity_id = $3::uuid
order by created_at desc
`

	rows, err := r.db.Query(queryCtx, query, companyID, entityType, entityID)
	if err != nil {
		return nil, fmt.Errorf("list stored objects by entity: %w", err)
	}
	defer rows.Close()

	objects := make([]domain.StoredObject, 0)
	for rows.Next() {
		var out domain.StoredObject
		var confirmedAt, deletedAt *time.Time
		var sizeBytes *int64
		var checksum *string
		var uploadAttemptID *string

		if err := rows.Scan(
			&out.ID, &out.CompanyID, &out.StorageProvider, &out.BucketName, &out.ObjectKey,
			&out.EntityType, &out.EntityID, &out.Category, &out.OriginalFilename, &out.ContentType,
			&out.ExpectedSizeBytes, &sizeBytes, &checksum, &out.UploadedByID, &out.Status,
			&out.UploadURLExpiresAt, &uploadAttemptID, &out.CreatedAt, &confirmedAt, &deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan stored object: %w", err)
		}
		out.SizeBytes = sizeBytes
		out.ChecksumSHA256 = checksum
		out.ConfirmedAt = confirmedAt
		out.DeletedAt = deletedAt
		if uploadAttemptID != nil {
			out.UploadAttemptID = *uploadAttemptID
		}
		objects = append(objects, out)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stored objects: %w", err)
	}
	if objects == nil {
		objects = []domain.StoredObject{}
	}
	return objects, nil
}

func (r *StoredObjectRepository) ListDeletePending(ctx context.Context, cutoff time.Time, limit int) ([]domain.StoredObject, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
select id::text, company_id::text, storage_provider, bucket_name, object_key, entity_type,
       entity_id::text, category, original_filename, content_type, expected_size_bytes,
       size_bytes, checksum_sha256, uploaded_by_id::text, status, upload_url_expires_at,
       upload_attempt_id, created_at, confirmed_at, deleted_at
from public."storedObject"
where status = 'DELETE_PENDING' and deleted_at < $1::timestamptz
order by deleted_at asc
limit $2::int
`

	rows, err := r.db.Query(queryCtx, query, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list delete pending: %w", err)
	}
	defer rows.Close()

	objects := make([]domain.StoredObject, 0)
	for rows.Next() {
		var out domain.StoredObject
		var confirmedAt, deletedAt *time.Time
		var sizeBytes *int64
		var checksum *string
		var uploadAttemptID *string

		if err := rows.Scan(
			&out.ID, &out.CompanyID, &out.StorageProvider, &out.BucketName, &out.ObjectKey,
			&out.EntityType, &out.EntityID, &out.Category, &out.OriginalFilename, &out.ContentType,
			&out.ExpectedSizeBytes, &sizeBytes, &checksum, &out.UploadedByID, &out.Status,
			&out.UploadURLExpiresAt, &uploadAttemptID, &out.CreatedAt, &confirmedAt, &deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan stored object: %w", err)
		}
		out.SizeBytes = sizeBytes
		out.ChecksumSHA256 = checksum
		out.ConfirmedAt = confirmedAt
		out.DeletedAt = deletedAt
		if uploadAttemptID != nil {
			out.UploadAttemptID = *uploadAttemptID
		}
		objects = append(objects, out)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stored objects: %w", err)
	}
	if objects == nil {
		objects = []domain.StoredObject{}
	}
	return objects, nil
}

func (r *StoredObjectRepository) List(ctx context.Context, filter StoredObjectFilter) ([]domain.StoredObject, int, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	args := make([]any, 0)
	conditions := make([]string, 0)
	argIdx := 1

	addCondition := func(cond string, val any) {
		conditions = append(conditions, cond)
		args = append(args, val)
		argIdx++
	}

	if filter.CompanyID != "" {
		addCondition(fmt.Sprintf("company_id = $%d::uuid", argIdx), filter.CompanyID)
	}
	if filter.EntityType != "" {
		addCondition(fmt.Sprintf("entity_type = $%d", argIdx), filter.EntityType)
	}
	if filter.EntityID != "" {
		addCondition(fmt.Sprintf("entity_id = $%d::uuid", argIdx), filter.EntityID)
	}
	if filter.Category != "" {
		addCondition(fmt.Sprintf("category = $%d", argIdx), filter.Category)
	}
	if filter.Status != "" {
		addCondition(fmt.Sprintf("status = $%d", argIdx), filter.Status)
	}
	if filter.UploadedByID != "" {
		addCondition(fmt.Sprintf("uploaded_by_id = $%d::uuid", argIdx), filter.UploadedByID)
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "where " + strings.Join(conditions, " and ")
	}

	countQuery := fmt.Sprintf(`
select count(*) from public."storedObject" %s
`, whereClause)

	var total int
	err := r.db.QueryRow(queryCtx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count stored objects: %w", err)
	}

	dataQuery := fmt.Sprintf(`
select id::text, company_id::text, storage_provider, bucket_name, object_key, entity_type,
       entity_id::text, category, original_filename, content_type, expected_size_bytes,
       size_bytes, checksum_sha256, uploaded_by_id::text, status, upload_url_expires_at,
       upload_attempt_id, created_at, confirmed_at, deleted_at
from public."storedObject"
%s
order by created_at desc
limit $%d offset $%d
`, whereClause, argIdx, argIdx+1)

	args = append(args, filter.Limit, filter.Offset)

	rows, err := r.db.Query(queryCtx, dataQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list stored objects: %w", err)
	}
	defer rows.Close()

	objects := make([]domain.StoredObject, 0)
	for rows.Next() {
		var out domain.StoredObject
		var confirmedAt, deletedAt *time.Time
		var sizeBytes *int64
		var checksum *string
		var uploadAttemptID *string

		if err := rows.Scan(
			&out.ID, &out.CompanyID, &out.StorageProvider, &out.BucketName, &out.ObjectKey,
			&out.EntityType, &out.EntityID, &out.Category, &out.OriginalFilename, &out.ContentType,
			&out.ExpectedSizeBytes, &sizeBytes, &checksum, &out.UploadedByID, &out.Status,
			&out.UploadURLExpiresAt, &uploadAttemptID, &out.CreatedAt, &confirmedAt, &deletedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan stored object: %w", err)
		}
		out.SizeBytes = sizeBytes
		out.ChecksumSHA256 = checksum
		out.ConfirmedAt = confirmedAt
		out.DeletedAt = deletedAt
		if uploadAttemptID != nil {
			out.UploadAttemptID = *uploadAttemptID
		}
		objects = append(objects, out)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read stored objects: %w", err)
	}
	if objects == nil {
		objects = []domain.StoredObject{}
	}
	return objects, total, nil
}

func (r *StoredObjectRepository) UpdateStatus(ctx context.Context, id string, status string, sizeBytes *int64, confirmedAt *time.Time) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
update public."storedObject"
set status = $1,
    confirmed_at = coalesce($2, confirmed_at),
    size_bytes = coalesce($3, size_bytes)
where id = $4::uuid
`

	result, err := r.db.Exec(queryCtx, query, status, confirmedAt, sizeBytes, id)
	if err != nil {
		return fmt.Errorf("update stored object status: %w", err)
	}
	if result.RowsAffected() == 0 {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *StoredObjectRepository) MarkDeletePending(ctx context.Context, id string, companyID string) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
update public."storedObject"
set status = 'DELETE_PENDING', deleted_at = now()
where id = $1::uuid and company_id = $2::uuid and status <> 'DELETED'
`

	result, err := r.db.Exec(queryCtx, query, id, companyID)
	if err != nil {
		return fmt.Errorf("mark delete pending: %w", err)
	}
	if result.RowsAffected() == 0 {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *StoredObjectRepository) ListPendingExpired(ctx context.Context, before time.Time, limit int) ([]domain.StoredObject, error) {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `
select id::text, company_id::text, storage_provider, bucket_name, object_key, entity_type,
       entity_id::text, category, original_filename, content_type, expected_size_bytes,
       size_bytes, checksum_sha256, uploaded_by_id::text, status, upload_url_expires_at,
       upload_attempt_id, created_at, confirmed_at, deleted_at
from public."storedObject"
where status = 'PENDING' and upload_url_expires_at < $1::timestamptz
order by upload_url_expires_at asc
limit $2::int
`

	rows, err := r.db.Query(queryCtx, query, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending expired: %w", err)
	}
	defer rows.Close()

	objects := make([]domain.StoredObject, 0)
	for rows.Next() {
		var out domain.StoredObject
		var confirmedAt, deletedAt *time.Time
		var sizeBytes *int64
		var checksum *string
		var uploadAttemptID *string

		if err := rows.Scan(
			&out.ID, &out.CompanyID, &out.StorageProvider, &out.BucketName, &out.ObjectKey,
			&out.EntityType, &out.EntityID, &out.Category, &out.OriginalFilename, &out.ContentType,
			&out.ExpectedSizeBytes, &sizeBytes, &checksum, &out.UploadedByID, &out.Status,
			&out.UploadURLExpiresAt, &uploadAttemptID, &out.CreatedAt, &confirmedAt, &deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan stored object: %w", err)
		}
		out.SizeBytes = sizeBytes
		out.ChecksumSHA256 = checksum
		out.ConfirmedAt = confirmedAt
		out.DeletedAt = deletedAt
		if uploadAttemptID != nil {
			out.UploadAttemptID = *uploadAttemptID
		}
		objects = append(objects, out)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stored objects: %w", err)
	}
	if objects == nil {
		objects = []domain.StoredObject{}
	}
	return objects, nil
}

func (r *StoredObjectRepository) Delete(ctx context.Context, id string) error {
	queryCtx, cancel := r.withTimeout(ctx)
	defer cancel()

	query := `delete from public."storedObject" where id = $1::uuid`
	result, err := r.db.Exec(queryCtx, query, id)
	if err != nil {
		return fmt.Errorf("delete stored object: %w", err)
	}
	if result.RowsAffected() == 0 {
		return outbound.ErrRecordNotFound
	}
	return nil
}

func (r *StoredObjectRepository) ListExpiredPending(ctx context.Context, before time.Time, limit int) ([]domain.StoredObject, error) {
	return r.ListPendingExpired(ctx, before, limit)
}

// Asegurar que el archivo compile incluso si json no se usa directamente aquí.
var _ = json.Marshal
