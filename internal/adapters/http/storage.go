package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

var ErrStorageForbidden = errors.New("storage operation forbidden")

const maxStorageRequestBody = 64 << 20

type storageUseCases interface {
	CreateUploadURL(ctx context.Context, user domain.User, input application.StorageCreateUploadInput) (domain.StoredObject, string, error)
	ConfirmUpload(ctx context.Context, user domain.User, input application.StorageConfirmInput) (domain.StoredObject, error)
	GetDownloadURL(ctx context.Context, user domain.User, input application.StorageDownloadURLInput) (string, error)
	DeleteObject(ctx context.Context, user domain.User, input application.StorageDeleteInput) error
	ListByEntity(ctx context.Context, user domain.User, companyID, entityType, entityID string) ([]domain.StoredObject, error)
}

type storageHandler struct {
	guard     resourceGuard
	service   storageUseCases
	logger    *slog.Logger
	cfg       config.StorageConfig
	fileStore outbound.FileStore
}

func newStorageHandler(auth authUseCases, service storageUseCases, cfg config.AuthConfig, storageCfg config.StorageConfig, logger *slog.Logger, fileStore outbound.FileStore) *storageHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &storageHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger, cfg: storageCfg, fileStore: fileStore}
}

func registerStorageRoutes(mux *http.ServeMux, auth authUseCases, service storageUseCases, authCfg config.AuthConfig, storageCfg config.StorageConfig, logger *slog.Logger, fileStore outbound.FileStore, repositories ...outbound.IdempotencyRepository) {
	if service == nil {
		return
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := newStorageHandler(auth, service, authCfg, storageCfg, logger, fileStore)

	mux.HandleFunc("POST /api/v1/storage/signed-url", idempotentMutation(handler.guard, idempotency, handler.logger, handler.createUploadURL))
	mux.HandleFunc("POST /api/v1/storage/objects/{objectId}/confirm", idempotentMutation(handler.guard, idempotency, handler.logger, handler.confirmUpload))
	mux.HandleFunc("GET /api/v1/storage/objects/{objectId}/download-url", handler.getDownloadURL)
	mux.HandleFunc("DELETE /api/v1/storage/objects/{objectId}", idempotentMutation(handler.guard, idempotency, handler.logger, handler.deleteObject))
	mux.HandleFunc("GET /api/v1/storage/objects", handler.listByEntity)

	// Rutas locales para pruebas con adaptador filesystem.
	if storageCfg.Provider == domain.StorageProviderLocal && fileStore != nil {
		mux.HandleFunc("PUT /api/v1/storage/local/upload", handler.localUpload)
		mux.HandleFunc("GET /api/v1/storage/local/download", handler.localDownload)
	}
}

func (h *storageHandler) createUploadURL(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	if r.Body == nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud requerido.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxStorageRequestBody+1))
	if err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "No se pudo leer el cuerpo de la solicitud.")
		return
	}
	if len(body) > maxStorageRequestBody {
		writeResourceError(h.logger, w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "El cuerpo de la solicitud supera el tamaño permitido.")
		return
	}

	var input struct {
		CompanyID         string `json:"companyId"`
		EntityType        string `json:"entityType"`
		EntityID          string `json:"entityId"`
		Category          string `json:"category"`
		OriginalFilename  string `json:"originalFilename"`
		ContentType       string `json:"contentType"`
		ExpectedSizeBytes int64  `json:"expectedSizeBytes"`
		UploadedByID      string `json:"uploadedById"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}
	if input.UploadedByID == "" {
		input.UploadedByID = session.User.ID
	}

	obj, uploadURL, err := h.service.CreateUploadURL(r.Context(), session.User, application.StorageCreateUploadInput{
		CompanyID:         input.CompanyID,
		EntityType:        input.EntityType,
		EntityID:          input.EntityID,
		Category:          input.Category,
		OriginalFilename:  input.OriginalFilename,
		ContentType:       input.ContentType,
		ExpectedSizeBytes: input.ExpectedSizeBytes,
		UploadedByID:      input.UploadedByID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"objectId":  obj.ID,
			"uploadUrl": uploadURL,
			"expiresAt": obj.UploadURLExpiresAt,
			"objectKey": obj.ObjectKey,
			"bucket":    obj.BucketName,
		},
	})
}

func (h *storageHandler) confirmUpload(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxStorageRequestBody+1))
	if err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "No se pudo leer el cuerpo de la solicitud.")
		return
	}

	var input struct {
		ObjectID  string `json:"objectId"`
		CompanyID string `json:"companyId"`
		SizeBytes int64  `json:"sizeBytes"`
		Checksum  string `json:"checksumSha256"`
	}
	if err := json.Unmarshal(body, &input); err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}

	obj, err := h.service.ConfirmUpload(r.Context(), session.User, application.StorageConfirmInput{
		ObjectID:  input.ObjectID,
		CompanyID: input.CompanyID,
		SizeBytes: input.SizeBytes,
		Checksum:  input.Checksum,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"objectId":    obj.ID,
			"status":      obj.Status,
			"sizeBytes":   obj.SizeBytes,
			"confirmedAt": obj.ConfirmedAt,
		},
	})
}

func (h *storageHandler) getDownloadURL(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}

	objectID := r.PathValue("objectId")
	if objectID == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de objeto requerido.")
		return
	}

	companyID := r.URL.Query().Get("companyId")
	if companyID == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "companyId requerido.")
		return
	}

	filename := r.URL.Query().Get("filename")

	downloadURL, err := h.service.GetDownloadURL(r.Context(), session.User, application.StorageDownloadURLInput{
		ObjectID:  objectID,
		CompanyID: companyID,
		Filename:  filename,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"downloadUrl": downloadURL,
		},
	})
}

func (h *storageHandler) deleteObject(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	objectID := r.PathValue("objectId")
	if objectID == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de objeto requerido.")
		return
	}

	companyID := r.URL.Query().Get("companyId")
	if companyID == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "companyId requerido.")
		return
	}

	if err := h.service.DeleteObject(r.Context(), session.User, application.StorageDeleteInput{
		ObjectID:  objectID,
		CompanyID: companyID,
	}); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"objectId": objectID,
			"status":   "deleted",
		},
	})
}

func (h *storageHandler) listByEntity(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}

	companyID := r.URL.Query().Get("companyId")
	entityType := r.URL.Query().Get("entityType")
	entityID := r.URL.Query().Get("entityId")

	if companyID == "" || entityType == "" || entityID == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "companyId, entityType y entityId son requeridos.")
		return
	}

	objects, err := h.service.ListByEntity(r.Context(), session.User, companyID, entityType, entityID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": objects,
	})
}

// Handlers para adaptador local (pruebas sin R2).

func (h *storageHandler) localUpload(w http.ResponseWriter, r *http.Request) {
	_, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	bucket := r.URL.Query().Get("bucket")
	key := r.URL.Query().Get("key")
	if bucket == "" || key == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "bucket y key requeridos.")
		return
	}

	if h.fileStore == nil {
		writeResourceError(h.logger, w, r, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Adaptador de archivos local no disponible.")
		return
	}

	written, err := h.fileStore.StoreFile(bucket, key, r.Body, r.ContentLength)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"bucket":    bucket,
			"key":       key,
			"sizeBytes": written,
		},
	})
}

func (h *storageHandler) localDownload(w http.ResponseWriter, r *http.Request) {
	_, ok := h.guard.session(w, r)
	if !ok {
		return
	}

	bucket := r.URL.Query().Get("bucket")
	key := r.URL.Query().Get("key")
	if bucket == "" || key == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "bucket y key requeridos.")
		return
	}

	if h.fileStore == nil {
		writeResourceError(h.logger, w, r, http.StatusNotImplemented, "NOT_IMPLEMENTED", "Adaptador de archivos local no disponible.")
		return
	}

	rc, err := h.fileStore.ReadFile(bucket, key)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCategory) || errors.Is(err, outbound.ErrRecordNotFound) {
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Objeto no encontrado.")
			return
		}
		h.writeError(w, r, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		h.logger.Error("failed to stream local download", "request_id", RequestID(r.Context()), "error", err)
	}
}

func (h *storageHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrStorageForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No autorizado para este objeto.")
	case errors.Is(err, outbound.ErrRecordNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Objeto no encontrado.")
	case errors.Is(err, domain.ErrInvalidContentType):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Tipo de contenido no permitido.")
	case errors.Is(err, domain.ErrInvalidSizeBytes):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Tamaño inválido.")
	default:
		h.logger.Error("storage handler error", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Error interno del servidor.")
	}
}

// ...existing code...
