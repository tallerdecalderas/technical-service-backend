package storageadapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var (
	ErrR2NotConfigured = errors.New("R2 storage not configured")
)

// R2Adapter implementa ObjectStorage sobre Cloudflare R2 (S3-compatible).
type R2Adapter struct {
	client *s3.Client
	bucket string
	logger *slog.Logger
}

func NewR2Adapter(endpoint, bucket, accessKeyID, secretAccessKey, region string, logger *slog.Logger) (*R2Adapter, error) {
	if endpoint == "" || bucket == "" || accessKeyID == "" || secretAccessKey == "" {
		return nil, ErrR2NotConfigured
	}
	if region == "" {
		region = "auto"
	}

	cfg := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	return &R2Adapter{
		client: client,
		bucket: bucket,
		logger: logger,
	}, nil
}

func (a *R2Adapter) CreateUploadURL(ctx context.Context, spec outbound.UploadSpec) (outbound.PresignedRequest, error) {
	if !domain.IsAllowedMIME(spec.ContentType) {
		return outbound.PresignedRequest{}, domain.ErrInvalidContentType
	}
	if spec.ExpectedSizeBytes <= 0 {
		return outbound.PresignedRequest{}, domain.ErrInvalidSizeBytes
	}

	input := &s3.PutObjectInput{
		Bucket:      aws.String(a.bucket),
		Key:         aws.String(spec.Key),
		ContentType: aws.String(spec.ContentType),
		Metadata: map[string]string{
			"upload-id": spec.UploadAttemptID,
		},
	}

	presignClient := s3.NewPresignClient(a.client)
	req, err := presignClient.PresignPutObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = spec.ExpiresIn
	})
	if err != nil {
		return outbound.PresignedRequest{}, fmt.Errorf("presign put object: %w", err)
	}

	headers := make(map[string]string)
	for k, v := range req.SignedHeader {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	headers["Content-Type"] = spec.ContentType
	headers["If-None-Match"] = "*"
	if spec.UploadAttemptID != "" {
		headers["x-amz-meta-upload-id"] = spec.UploadAttemptID
	}

	return outbound.PresignedRequest{
		URL:       req.URL,
		Method:    http.MethodPut,
		Headers:   headers,
		ExpiresAt: time.Now().Add(spec.ExpiresIn),
	}, nil
}

func (a *R2Adapter) CreateDownloadURL(ctx context.Context, spec outbound.DownloadSpec) (outbound.PresignedRequest, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(spec.Key),
	}

	presignClient := s3.NewPresignClient(a.client)
	req, err := presignClient.PresignGetObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = spec.ExpiresIn
	})
	if err != nil {
		return outbound.PresignedRequest{}, fmt.Errorf("presign get object: %w", err)
	}

	return outbound.PresignedRequest{
		URL:       req.URL,
		Method:    http.MethodGet,
		Headers:   map[string]string{},
		ExpiresAt: time.Now().Add(spec.ExpiresIn),
	}, nil
}

func (a *R2Adapter) HeadObject(ctx context.Context, ref outbound.ObjectRef) (outbound.ObjectInfo, error) {
	input := &s3.HeadObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(ref.Key),
	}

	out, err := a.client.HeadObject(ctx, input)
	if err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return outbound.ObjectInfo{}, ErrObjectNotFound
		}
		return outbound.ObjectInfo{}, fmt.Errorf("head object: %w", err)
	}

	contentType := ""
	if out.ContentType != nil {
		contentType = *out.ContentType
	}

	metadata := map[string]string{}
	for k, v := range out.Metadata {
		metadata[k] = v
	}

	lastModified := time.Time{}
	if out.LastModified != nil {
		lastModified = *out.LastModified
	}

	var sizeBytes int64
	if out.ContentLength != nil {
		sizeBytes = *out.ContentLength
	}

	return outbound.ObjectInfo{
		Bucket:       a.bucket,
		Key:          ref.Key,
		SizeBytes:    sizeBytes,
		ContentType:  contentType,
		Metadata:     metadata,
		ETag:         "",
		LastModified: lastModified,
	}, nil
}

func (a *R2Adapter) DeleteObject(ctx context.Context, ref outbound.ObjectRef) error {
	input := &s3.DeleteObjectInput{
		Bucket: aws.String(a.bucket),
		Key:    aws.String(ref.Key),
	}
	_, err := a.client.DeleteObject(ctx, input)
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// ...existing code...
