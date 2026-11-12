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

const maxServiceExpenseRequestBytes = 64 << 10

type expenseUseCases interface {
	List(context.Context, domain.User, string) ([]domain.Expense, error)
	Create(context.Context, domain.User, application.ExpenseCreateInput) (domain.Expense, error)
	Update(context.Context, domain.User, string, string, domain.ExpensePatch) (domain.Expense, error)
	Delete(context.Context, domain.User, string, string) error
}

type serviceExpenseHandler struct {
	guard   resourceGuard
	service expenseUseCases
	logger  *slog.Logger
}

func newServiceExpenseHandler(auth authUseCases, service expenseUseCases, cfg config.AuthConfig, logger *slog.Logger) *serviceExpenseHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &serviceExpenseHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
}

func registerServiceExpenseRoutes(mux *http.ServeMux, auth authUseCases, service expenseUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) {
	if service == nil {
		return
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := newServiceExpenseHandler(auth, service, cfg, logger)
	mux.HandleFunc("GET /api/v1/services/{serviceId}/expenses", handler.list)
	mux.HandleFunc("POST /api/v1/services/{serviceId}/expenses", idempotentMutation(handler.guard, idempotency, handler.logger, handler.create))
	mux.HandleFunc("PATCH /api/v1/services/{serviceId}/expenses/{expenseId}", idempotentMutation(handler.guard, idempotency, handler.logger, handler.update))
	mux.HandleFunc("DELETE /api/v1/services/{serviceId}/expenses/{expenseId}", idempotentMutation(handler.guard, idempotency, handler.logger, handler.delete))
}

func (h *serviceExpenseHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), nil); err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	expenses, err := h.service.List(ctx, session.User, r.PathValue("serviceId"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": expenses})
}

func (h *serviceExpenseHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Concept  string `json:"concept"`
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}
	if !decodeServiceExpenseJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	expense, err := h.service.Create(ctx, session.User, application.ExpenseCreateInput{
		ServiceID: r.PathValue("serviceId"), Concept: body.Concept, Amount: body.Amount, Currency: body.Currency,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": expense})
}

func (h *serviceExpenseHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Concept  apiField[string] `json:"concept"`
		Amount   apiField[string] `json:"amount"`
		Currency apiField[string] `json:"currency"`
	}
	if !decodeServiceExpenseJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	expense, err := h.service.Update(ctx, session.User, r.PathValue("serviceId"), r.PathValue("expenseId"), domain.ExpensePatch{
		Concept:  domain.Field[string]{Set: body.Concept.Set, Value: body.Concept.Value},
		Amount:   domain.Field[string]{Set: body.Amount.Set, Value: body.Amount.Value},
		Currency: domain.Field[string]{Set: body.Currency.Set, Value: body.Currency.Value},
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": expense})
}

func (h *serviceExpenseHandler) delete(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := h.service.Delete(ctx, session.User, r.PathValue("serviceId"), r.PathValue("expenseId")); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeServiceExpenseJSON(r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxServiceExpenseRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func (h *serviceExpenseHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrExpenseForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para realizar esta operación.")
	case errors.Is(err, application.ErrInvalidExpense):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrExpenseNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Gasto o servicio no encontrado.")
	case errors.Is(err, application.ErrExpenseConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "El gasto entra en conflicto con una regla del negocio.")
	default:
		h.logger.Error("service expense operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ expenseUseCases = (*application.ExpenseUseCases)(nil)
