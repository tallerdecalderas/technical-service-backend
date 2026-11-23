package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

const maxCompanyRequestBytes = 1 << 20

const companyOperationTimeout = 5 * time.Second

type companyUseCases interface {
	List(context.Context, domain.User) ([]domain.CompanySummary, error)
	Create(context.Context, domain.User, domain.CompanyCreateInput) (domain.CompanySummary, error)
	Update(context.Context, domain.User, string, domain.CompanyUpdateInput) (domain.CompanySummary, error)
	Deactivate(context.Context, domain.User, string) error
}

type companyHandler struct {
	guard   resourceGuard
	service companyUseCases
	logger  *slog.Logger
}

func NewCompanyHandler(auth authUseCases, service companyUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &companyHandler{
		guard:   newResourceGuard(auth, cfg, logger),
		service: service,
		logger:  logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/companies", handler.list)
	mux.HandleFunc("POST /api/v1/companies", idempotentMutation(handler.guard, getIdempotency(repositories), logger, handler.create))
	mux.HandleFunc("PATCH /api/v1/companies/", idempotentMutation(handler.guard, getIdempotency(repositories), logger, handler.update))
	mux.HandleFunc("DELETE /api/v1/companies/", idempotentMutation(handler.guard, getIdempotency(repositories), logger, handler.deactivate))
	return mux
}

func getIdempotency(repositories []outbound.IdempotencyRepository) outbound.IdempotencyRepository {
	if len(repositories) > 0 {
		return repositories[0]
	}
	return nil
}

type companyCreateRequest struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Phone   string `json:"phone,omitempty"`
}

type companyUpdateRequest struct {
	Name    *string `json:"name,omitempty"`
	Address *string `json:"address,omitempty"`
	Phone   *string `json:"phone,omitempty"`
	IsActive *bool  `json:"isActive,omitempty"`
}

type companyResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"isActive"`
	Address   string    `json:"address,omitempty"`
	Phone     string    `json:"phone,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

func (h *companyHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), nil); err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "No se admiten filtros para esta consulta.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), companyOperationTimeout)
	defer cancel()
	companies, err := h.service.List(ctx, session.User)
	if err != nil {
		if errors.Is(err, application.ErrCompanyForbidden) {
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para consultar empresas.")
			return
		}
		h.logger.Error("company listing failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudieron cargar las empresas.")
		return
	}

	response := make([]companyResponse, 0, len(companies))
	for _, company := range companies {
		response = append(response, companyResponse{ID: company.ID, Name: company.Name})
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": response})
}

func (h *companyHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	var body companyCreateRequest
	if !decodeCompanyJSON(w, r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), companyOperationTimeout)
	defer cancel()

	summary, err := h.service.Create(ctx, session.User, domain.CompanyCreateInput{
		Name:    body.Name,
		Address: body.Address,
		Phone:   body.Phone,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrCompanyForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para crear empresas.")
		case errors.Is(err, application.ErrInvalidCompany):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "El nombre de la empresa es requerido.")
		default:
			h.logger.Error("company creation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo crear la empresa.")
		}
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": companyResponse{ID: summary.ID, Name: summary.Name}})
}

func (h *companyHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de empresa requerido.")
		return
	}

	var body companyUpdateRequest
	if !decodeCompanyJSON(w, r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), companyOperationTimeout)
	defer cancel()

	summary, err := h.service.Update(ctx, session.User, id, domain.CompanyUpdateInput{
		Name:     body.Name,
		Address:  body.Address,
		Phone:    body.Phone,
		IsActive: body.IsActive,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrCompanyForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para editar esta empresa.")
		case errors.Is(err, application.ErrInvalidCompany):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Datos de empresa inválidos.")
		case errors.Is(err, outbound.ErrRecordNotFound):
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Empresa no encontrada.")
		default:
			h.logger.Error("company update failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo actualizar la empresa.")
		}
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": companyResponse{ID: summary.ID, Name: summary.Name}})
}

func (h *companyHandler) deactivate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de empresa requerido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), companyOperationTimeout)
	defer cancel()

	if err := h.service.Deactivate(ctx, session.User, id); err != nil {
		switch {
		case errors.Is(err, application.ErrCompanyForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para desactivar esta empresa.")
		case errors.Is(err, application.ErrInvalidCompany):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de empresa inválido.")
		case errors.Is(err, outbound.ErrRecordNotFound):
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Empresa no encontrada.")
		default:
			h.logger.Error("company deactivation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo desactivar la empresa.")
		}
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": map[string]string{"id": id, "status": "deactivated"}})
}

func decodeCompanyJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxCompanyRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}
