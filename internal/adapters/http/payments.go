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

const maxPaymentRequestBytes = 64 << 10

type paymentUseCases interface {
	List(context.Context, domain.User, application.PaymentListInput) (application.PaymentListResult, error)
	Get(context.Context, domain.User, string) (domain.Payment, error)
	Create(context.Context, domain.User, application.PaymentCreateInput) (domain.Payment, error)
	Update(context.Context, domain.User, string, domain.PaymentPatch) (domain.Payment, error)
}

type paymentHandler struct {
	guard   resourceGuard
	service paymentUseCases
	logger  *slog.Logger
}

func NewPaymentHandler(auth authUseCases, service paymentUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &paymentHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/payments", handler.list)
	mux.HandleFunc("POST /api/v1/payments", idempotentMutation(handler.guard, idempotency, logger, handler.create))
	mux.HandleFunc("GET /api/v1/payments/{id}", handler.get)
	mux.HandleFunc("PATCH /api/v1/payments/{id}", idempotentMutation(handler.guard, idempotency, logger, handler.update))
	return mux
}

func (h *paymentHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	input, err := paymentListInput(r)
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

func (h *paymentHandler) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	payment, err := h.service.Get(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": payment})
}

func (h *paymentHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		ServiceID  string  `json:"serviceId"`
		Method     string  `json:"method"`
		AmountPaid string  `json:"amountPaid"`
		DebtAmount *string `json:"debtAmount"`
	}
	if !decodePaymentJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	debtAmount := "0"
	if body.DebtAmount != nil {
		debtAmount = *body.DebtAmount
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	payment, err := h.service.Create(ctx, session.User, application.PaymentCreateInput{
		ServiceID: body.ServiceID, Method: body.Method, AmountPaid: body.AmountPaid, DebtAmount: debtAmount,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": payment})
}

type optionalPaymentString struct {
	Set   bool
	Value *string
}

func (value *optionalPaymentString) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if string(raw) == "null" {
		return nil
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

func (h *paymentHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Method     optionalPaymentString `json:"method"`
		AmountPaid optionalPaymentString `json:"amountPaid"`
		DebtAmount optionalPaymentString `json:"debtAmount"`
	}
	if !decodePaymentJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	payment, err := h.service.Update(ctx, session.User, r.PathValue("id"), domain.PaymentPatch{
		Method:     domain.Field[string]{Set: body.Method.Set, Value: body.Method.Value},
		AmountPaid: domain.Field[string]{Set: body.AmountPaid.Set, Value: body.AmountPaid.Value},
		DebtAmount: domain.Field[string]{Set: body.DebtAmount.Set, Value: body.DebtAmount.Value},
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": payment})
}

func paymentListInput(r *http.Request) (application.PaymentListInput, error) {
	if err := rejectUnknownQuery(r.URL.Query(), []string{"technicianId", "page", "pageSize"}); err != nil {
		return application.PaymentListInput{}, err
	}
	page, pageSize, err := paginationQuery(r.URL.Query())
	if err != nil {
		return application.PaymentListInput{}, err
	}
	return application.PaymentListInput{TechnicianID: r.URL.Query().Get("technicianId"), Page: page, PageSize: pageSize}, nil
}

func decodePaymentJSON(r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxPaymentRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func (h *paymentHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrPaymentForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para realizar esta operación.")
	case errors.Is(err, application.ErrInvalidPayment):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrPaymentNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Cobro no encontrado.")
	case errors.Is(err, application.ErrPaymentConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "El cobro entra en conflicto con una regla del negocio.")
	case errors.Is(err, application.ErrPaymentRule):
		writeResourceError(h.logger, w, r, http.StatusUnprocessableEntity, "DOMAIN_RULE_VIOLATION", "La operación no cumple las reglas del cobro.")
	default:
		h.logger.Error("payment operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ paymentUseCases = (*application.PaymentUseCases)(nil)
