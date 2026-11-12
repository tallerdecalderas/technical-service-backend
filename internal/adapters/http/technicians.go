package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type technicianUseCases interface {
	List(context.Context, domain.User, application.TechnicianListInput) (application.TechnicianListResult, error)
	Get(context.Context, domain.User, string) (domain.Technician, error)
	Create(context.Context, domain.User, application.TechnicianCreateInput) (domain.Technician, error)
	Update(context.Context, domain.User, string, domain.TechnicianPatch) (domain.Technician, error)
}

type technicianHandler struct {
	guard   resourceGuard
	service technicianUseCases
	logger  *slog.Logger
}

func NewTechnicianHandler(auth authUseCases, service technicianUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &technicianHandler{
		guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/technicians", handler.list)
	mux.HandleFunc("POST /api/v1/technicians", idempotentMutation(
		handler.guard, idempotency, logger, handler.create,
	))
	mux.HandleFunc("GET /api/v1/technicians/{id}", handler.get)
	mux.HandleFunc("PATCH /api/v1/technicians/{id}", idempotentMutation(
		handler.guard, idempotency, logger, handler.update,
	))
	return mux
}

func (h *technicianHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), []string{"page", "pageSize"}); err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
		return
	}
	page, pageSize, err := paginationQuery(r.URL.Query())
	if err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := h.service.List(ctx, session.User, application.TechnicianListInput{Page: page, PageSize: pageSize})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": result.Data,
		"meta": map[string]int{"page": result.Page, "pageSize": result.PageSize, "total": result.Total},
	})
}

func (h *technicianHandler) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	technician, err := h.service.Get(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": technician})
}

func (h *technicianHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Name       string   `json:"name"`
		Email      string   `json:"email"`
		Password   string   `json:"password"`
		Phone      *string  `json:"phone"`
		DNI        *string  `json:"dni"`
		CompanyIDs []string `json:"companyIds"`
	}
	if !decodeClientJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	technician, err := h.service.Create(ctx, session.User, application.TechnicianCreateInput{
		Name: body.Name, Email: body.Email, Password: body.Password,
		Phone: body.Phone, DNI: body.DNI, CompanyIDs: body.CompanyIDs,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": technician})
}

type apiField[T any] struct {
	Set   bool
	Value *T
}

func (field *apiField[T]) UnmarshalJSON(raw []byte) error {
	field.Set = true
	if string(raw) == "null" {
		field.Value = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	field.Value = &value
	return nil
}

func (h *technicianHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Name       apiField[string]   `json:"name"`
		Phone      apiField[string]   `json:"phone"`
		DNI        apiField[string]   `json:"dni"`
		Active     apiField[bool]     `json:"isActive"`
		Avatar     apiField[string]   `json:"avatar"`
		CompanyIDs apiField[[]string] `json:"companyIds"`
	}
	if !decodeClientJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	patch := domain.TechnicianPatch{
		Name:       domain.Field[string]{Set: body.Name.Set, Value: body.Name.Value},
		Phone:      domain.Field[string]{Set: body.Phone.Set, Value: body.Phone.Value},
		DNI:        domain.Field[string]{Set: body.DNI.Set, Value: body.DNI.Value},
		Active:     domain.Field[bool]{Set: body.Active.Set, Value: body.Active.Value},
		Avatar:     domain.Field[string]{Set: body.Avatar.Set, Value: body.Avatar.Value},
		CompanyIDs: domain.Field[[]string]{Set: body.CompanyIDs.Set, Value: body.CompanyIDs.Value},
	}
	if patch.Avatar.Set && patch.Avatar.Value != nil {
		parsed, err := url.ParseRequestURI(*patch.Avatar.Value)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "El avatar debe ser una URL HTTPS válida.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	technician, err := h.service.Update(ctx, session.User, r.PathValue("id"), patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": technician})
}

func (h *technicianHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrTechnicianForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para realizar esta operación.")
	case errors.Is(err, application.ErrInvalidTechnician):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrTechnicianNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Técnico no encontrado.")
	case errors.Is(err, application.ErrTechnicianConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "El correo ya está registrado o el técnico entra en conflicto con otro registro.")
	case errors.Is(err, application.ErrTechnicianUnavailable):
		h.logger.Error("technician identity provisioning failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusBadGateway, "IDENTITY_PROVIDER_UNAVAILABLE", "No se pudo crear la identidad del técnico.")
	default:
		h.logger.Error("technician operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ technicianUseCases = (*application.TechnicianUseCases)(nil)
