package httpadapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type catalogImportUseCases interface {
	ImportCSV(context.Context, domain.User, application.CatalogCSVImportInput) (domain.CatalogImport, error)
	Import(context.Context, domain.User, application.CatalogImportInput) (domain.CatalogImport, error)
}

type catalogImportHandler struct {
	guard   resourceGuard
	service catalogImportUseCases
	logger  *slog.Logger
}

func NewCatalogImportHandler(auth authUseCases, service catalogImportUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &catalogImportHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/catalog/import",
		idempotentMutation(handler.guard, idempotency, logger, handler.importCatalog))
	mux.HandleFunc("POST /api/v1/integrations/google-sheets/catalog-import",
		idempotentMutation(handler.guard, idempotency, logger, handler.importCatalog))
	return mux
}

func (h *catalogImportHandler) importCatalog(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		const maxUploadBytes = 10 << 20 // 10MB
		if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Error al procesar el archivo subido.")
			return
		}
		companyID := strings.TrimSpace(r.FormValue("companyId"))
		if companyID == "" {
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "El campo companyId es obligatorio.")
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			file, header, err = r.FormFile("csv")
		}
		if err != nil {
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Debés adjuntar un archivo CSV en el campo 'file'.")
			return
		}
		defer file.Close()

		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()

		imported, err := h.service.ImportCSV(ctx, session.User, application.CatalogCSVImportInput{
			CompanyID: companyID,
			FileName:  header.Filename,
			Reader:    file,
		})
		if err != nil {
			var validation *application.CatalogImportValidationError
			if errors.As(err, &validation) {
				writeJSON(h.logger, w, http.StatusUnprocessableEntity, map[string]any{
					"error": map[string]any{
						"code": "VALIDATION_ERROR", "message": "La importación no se publicó porque el archivo tiene errores.",
						"details": validation.Import.Errors, "requestId": RequestID(r.Context()),
					},
					"data": validation.Import,
				})
				return
			}
			h.writeError(w, r, err)
			return
		}
		writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": imported})
		return
	}

	var body struct {
		CompanyID string `json:"companyId"`
	}
	if !decodePaymentJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados. Se requiere multipart/form-data con archivo CSV o JSON con companyId.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	imported, err := h.service.Import(ctx, session.User, application.CatalogImportInput{CompanyID: body.CompanyID})
	if err != nil {
		var validation *application.CatalogImportValidationError
		if errors.As(err, &validation) {
			writeJSON(h.logger, w, http.StatusUnprocessableEntity, map[string]any{
				"error": map[string]any{
					"code": "VALIDATION_ERROR", "message": "La importación no se publicó porque la hoja tiene errores.",
					"details": validation.Import.Errors, "requestId": RequestID(r.Context()),
				},
				"data": validation.Import,
			})
			return
		}
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": imported})
}

func (h *catalogImportHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrCatalogImportForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "Solo Administración puede importar el catálogo.")
	case errors.Is(err, application.ErrInvalidCatalogImport):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá la empresa de la importación.")
	case errors.Is(err, application.ErrCatalogImportUnavailable):
		writeResourceError(h.logger, w, r, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "No se pudo leer la hoja de cálculo configurada.")
	case errors.Is(err, application.ErrCatalogImportConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "La importación entra en conflicto con el catálogo actual.")
	case errors.Is(err, application.ErrCatalogImportRule), errors.Is(err, outbound.ErrRuleViolation):
		writeResourceError(h.logger, w, r, http.StatusUnprocessableEntity, "DOMAIN_RULE_VIOLATION", "Falta una cotización del dólar o la migración del catálogo antes de importar.")
	default:
		h.logger.Error("catalog import failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la importación.")
	}
}

var _ catalogImportUseCases = (*application.CatalogImportUseCases)(nil)
