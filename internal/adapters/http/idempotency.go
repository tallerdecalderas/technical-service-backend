package httpadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"technical-service-backend/internal/ports/outbound"
)

const (
	maxIdempotencyKeyLength     = 128
	maxIdempotencyRequestBytes  = 10 << 20
	maxIdempotencyResponseBytes = 1 << 20
	idempotencyWaitTimeout      = 5 * time.Second
	idempotencyPollInterval     = 50 * time.Millisecond
)

func idempotentMutation(
	guard resourceGuard,
	repository outbound.IdempotencyRepository,
	logger *slog.Logger,
	next http.HandlerFunc,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session, ok := guard.mutationSession(w, r)
		if !ok {
			return
		}
		if repository == nil {
			logger.Error("idempotency repository is unavailable", "request_id", RequestID(r.Context()))
			writeResourceError(logger, w, r, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", "No se pudo validar la clave de operación.")
			return
		}

		keyValue := r.Header.Get("Idempotency-Key")
		if keyValue == "" || utf8.RuneCountInString(keyValue) > maxIdempotencyKeyLength {
			writeResourceError(logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "La clave Idempotency-Key debe tener entre 1 y 128 caracteres.")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxIdempotencyRequestBytes+1))
		if err != nil {
			writeResourceError(logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "No se pudo leer el cuerpo de la solicitud.")
			return
		}
		if len(body) > maxIdempotencyRequestBytes {
			writeResourceError(logger, w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "El cuerpo de la solicitud supera el tamaño permitido.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		contentType := r.Header.Get("Content-Type")
		hashInput := make([]byte, 0, len(r.Method)+len(r.URL.RequestURI())+len(contentType)+len(body)+3)
		hashInput = append(hashInput, r.Method...)
		hashInput = append(hashInput, '\n')
		hashInput = append(hashInput, r.URL.RequestURI()...)
		hashInput = append(hashInput, '\n')
		hashInput = append(hashInput, contentType...)
		hashInput = append(hashInput, '\n')
		hashInput = append(hashInput, body...)
		digest := sha256.Sum256(hashInput)
		key := outbound.IdempotencyKey{
			UserID: session.User.ID, Operation: r.Method + " " + r.URL.Path,
			Key: keyValue, RequestHash: hex.EncodeToString(digest[:]),
		}

		record, created, err := repository.Claim(r.Context(), key)
		if err != nil {
			logger.Error("idempotency claim failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(logger, w, r, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", "No se pudo validar la clave de operación.")
			return
		}
		if record.RequestHash != key.RequestHash {
			writeResourceError(logger, w, r, http.StatusConflict, "CONFLICT", "La clave Idempotency-Key ya se usó con otros datos.")
			return
		}
		if !created {
			record, ready, err := awaitIdempotencyResult(r.Context(), repository, key, record)
			if err != nil {
				logger.Error("idempotency result lookup failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
				writeResourceError(logger, w, r, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", "No se pudo recuperar el resultado de la operación.")
				return
			}
			if !ready {
				writeResourceError(logger, w, r, http.StatusConflict, "IDEMPOTENCY_IN_PROGRESS", "La operación con esta clave todavía está en curso.")
				return
			}
			writeIdempotencyResponse(logger, w, record.StatusCode, record.ContentType, record.Response)
			return
		}

		keepClaim := false
		defer func() {
			if keepClaim {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := repository.Release(cleanupCtx, key); err != nil {
				logger.Error("idempotency claim cleanup failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			}
		}()

		captured := newIdempotencyResponseWriter()
		next.ServeHTTP(captured, r)
		if captured.overflow {
			writeResourceError(logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo guardar el resultado de la operación.")
			return
		}
		if err := repository.Complete(r.Context(), key, captured.statusCode(), captured.header.Get("Content-Type"), captured.body.Bytes()); err != nil {
			keepClaim = true
			logger.Error("idempotency result persistence failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(logger, w, r, http.StatusServiceUnavailable, "IDEMPOTENCY_UNAVAILABLE", "La operación no pudo confirmarse para reintentos seguros.")
			return
		}
		keepClaim = true
		captured.writeTo(logger, w)
	}
}

func awaitIdempotencyResult(
	ctx context.Context,
	repository outbound.IdempotencyRepository,
	key outbound.IdempotencyKey,
	record outbound.IdempotencyRecord,
) (outbound.IdempotencyRecord, bool, error) {
	if record.Completed {
		return record, true, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, idempotencyWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(idempotencyPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return outbound.IdempotencyRecord{}, false, ctx.Err()
			}
			return record, false, nil
		case <-ticker.C:
			next, created, err := repository.Claim(waitCtx, key)
			if err != nil {
				if waitCtx.Err() != nil && ctx.Err() == nil {
					return record, false, nil
				}
				return outbound.IdempotencyRecord{}, false, err
			}
			if created {
				return outbound.IdempotencyRecord{}, false, errors.New("idempotency claim unexpectedly became new")
			}
			if next.RequestHash != key.RequestHash {
				return outbound.IdempotencyRecord{}, false, errors.New("idempotency request hash changed")
			}
			if next.Completed {
				return next, true, nil
			}
		}
	}
}

type idempotencyResponseWriter struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func newIdempotencyResponseWriter() *idempotencyResponseWriter {
	return &idempotencyResponseWriter{header: make(http.Header)}
}

func (w *idempotencyResponseWriter) Header() http.Header {
	return w.header
}

func (w *idempotencyResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *idempotencyResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(body) > maxIdempotencyResponseBytes {
		w.overflow = true
		return 0, fmt.Errorf("idempotency response exceeds limit")
	}
	return w.body.Write(body)
}

func (w *idempotencyResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *idempotencyResponseWriter) writeTo(logger *slog.Logger, target http.ResponseWriter) {
	if contentType := w.header.Get("Content-Type"); contentType != "" {
		target.Header().Set("Content-Type", contentType)
	}
	if cacheControl := w.header.Get("Cache-Control"); cacheControl != "" {
		target.Header().Set("Cache-Control", cacheControl)
	}
	target.WriteHeader(w.statusCode())
	if _, err := target.Write(w.body.Bytes()); err != nil {
		logger.Error("failed to replay idempotent HTTP response", "error", fmt.Sprintf("%T", err))
	}
}

func writeIdempotencyResponse(logger *slog.Logger, w http.ResponseWriter, status int, contentType string, body []byte) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		logger.Error("failed to write idempotent HTTP response", "error", fmt.Sprintf("%T", err))
	}
}
