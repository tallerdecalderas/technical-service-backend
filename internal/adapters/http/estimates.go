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

const maxEstimateRequestBytes = 64 << 10

type estimateUseCases interface {
	List(context.Context, domain.User, application.EstimateListInput) (application.EstimateListResult, error)
	Get(context.Context, domain.User, string) (domain.Estimate, error)
	Create(context.Context, domain.User, application.EstimateCreateInput) (domain.Estimate, error)
	Finalize(context.Context, domain.User, string) (domain.Estimate, error)
}

type estimateHandler struct {
	guard   resourceGuard
	service estimateUseCases
	logger  *slog.Logger
}

func NewEstimateHandler(auth authUseCases, service estimateUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &estimateHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/estimates", handler.list)
	mux.HandleFunc("POST /api/v1/estimates", idempotentMutation(handler.guard, idempotency, logger, handler.create))
	mux.HandleFunc("GET /api/v1/estimates/{id}", handler.get)
	mux.HandleFunc("POST /api/v1/estimates/{id}/finalize", idempotentMutation(handler.guard, idempotency, logger, handler.finalize))
	return mux
}

func (h *estimateHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	input, err := estimateListInput(r)
	if err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := h.service.List(ctx, session.User, input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": result.Data,
		"meta": map[string]int{"page": result.Page, "pageSize": result.PageSize, "total": result.Total},
	})
}

func (h *estimateHandler) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	estimate, err := h.service.Get(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": estimate})
}

func (h *estimateHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		ServiceID   string  `json:"serviceId"`
		Amount      string  `json:"amount"`
		Description *string `json:"description"`
		Notes       *string `json:"notes"`
	}
	if !decodeEstimateJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	estimate, err := h.service.Create(ctx, session.User, application.EstimateCreateInput{
		ServiceID: body.ServiceID, Amount: body.Amount, Description: body.Description, Notes: body.Notes,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": estimate})
}

func (h *estimateHandler) finalize(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	estimate, err := h.service.Finalize(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": estimate})
}

func estimateListInput(r *http.Request) (application.EstimateListInput, error) {
	if err := rejectUnknownQuery(r.URL.Query(), []string{"status", "technicianId", "page", "pageSize"}); err != nil {
		return application.EstimateListInput{}, err
	}
	page, pageSize, err := paginationQuery(r.URL.Query())
	if err != nil {
		return application.EstimateListInput{}, err
	}
	return application.EstimateListInput{
		Status: r.URL.Query().Get("status"), TechnicianID: r.URL.Query().Get("technicianId"),
		Page: page, PageSize: pageSize,
	}, nil
}

func decodeEstimateJSON(r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxEstimateRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func (h *estimateHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrEstimateForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para realizar esta operación.")
	case errors.Is(err, application.ErrInvalidEstimate):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrEstimateNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Presupuesto no encontrado.")
	case errors.Is(err, application.ErrEstimateConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "El presupuesto entra en conflicto con una regla del negocio.")
	case errors.Is(err, application.ErrEstimateRule):
		writeResourceError(h.logger, w, r, http.StatusUnprocessableEntity, "DOMAIN_RULE_VIOLATION", "La operación no cumple las reglas del presupuesto.")
	default:
		h.logger.Error("estimate operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ estimateUseCases = (*application.EstimateUseCases)(nil)
