package httpadapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type serviceUseCases interface {
	List(context.Context, domain.User, application.ServiceListInput) (application.ServiceListResult, error)
	Get(context.Context, domain.User, string) (domain.Service, error)
	History(context.Context, domain.User, string, int, int) (domain.ServiceHistoryResult, error)
	Create(context.Context, domain.User, application.ServiceCreateInput) (domain.Service, error)
	Update(context.Context, domain.User, string, domain.ServicePatch) (domain.Service, error)
	ChangeStatus(context.Context, domain.User, string, string) (domain.Service, error)
	Finish(context.Context, domain.User, string, domain.ServiceFinish) (domain.Service, error)
}

type serviceHandler struct {
	guard   resourceGuard
	service serviceUseCases
	logger  *slog.Logger
}

func NewServiceHandler(auth authUseCases, service serviceUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	return newServiceHandler(auth, service, nil, nil, cfg, logger, repositories...)
}

func NewServiceHandlerWithParts(auth authUseCases, service serviceUseCases, parts servicePartUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	return newServiceHandler(auth, service, parts, nil, cfg, logger, repositories...)
}

func NewServiceHandlerWithPartsAndExpenses(
	auth authUseCases,
	service serviceUseCases,
	parts servicePartUseCases,
	expenses expenseUseCases,
	cfg config.AuthConfig,
	logger *slog.Logger,
	repositories ...outbound.IdempotencyRepository,
) http.Handler {
	return newServiceHandler(auth, service, parts, expenses, cfg, logger, repositories...)
}

func newServiceHandler(auth authUseCases, service serviceUseCases, parts servicePartUseCases, expenses expenseUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &serviceHandler{
		guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/services", handler.list)
	mux.HandleFunc("POST /api/v1/services", idempotentMutation(
		handler.guard, idempotency, logger, handler.create,
	))
	mux.HandleFunc("GET /api/v1/services/{id}", handler.get)
	mux.HandleFunc("PATCH /api/v1/services/{id}", idempotentMutation(
		handler.guard, idempotency, logger, handler.update,
	))
	mux.HandleFunc("POST /api/v1/services/{id}/status", idempotentMutation(
		handler.guard, idempotency, logger, handler.changeStatus,
	))
	mux.HandleFunc("POST /api/v1/services/{id}/finish", idempotentMutation(
		handler.guard, idempotency, logger, handler.finish,
	))
	mux.HandleFunc("GET /api/v1/services/{id}/history", handler.history)
	registerServicePartRoutes(mux, auth, parts, cfg, logger, idempotency)
	registerServiceExpenseRoutes(mux, auth, expenses, cfg, logger, idempotency)
	mux.HandleFunc("GET /api/v1/schedule", handler.list)
	return mux
}

func (h *serviceHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	input, err := serviceListInput(r)
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

func (h *serviceHandler) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	service, err := h.service.Get(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": service})
}

func (h *serviceHandler) history(w http.ResponseWriter, r *http.Request) {
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
	result, err := h.service.History(ctx, session.User, r.PathValue("id"), page, pageSize)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": result.Data,
		"meta": map[string]int{"page": result.Page, "pageSize": result.PageSize, "total": result.Total},
	})
}

func (h *serviceHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		CompanyID      string  `json:"companyId"`
		ClientID       string  `json:"clientId"`
		CategoryID     string  `json:"categoryId"`
		TechnicianID   *string `json:"technicianId"`
		ScheduledDate  string  `json:"scheduledDate"`
		Address        string  `json:"address"`
		Locality       string  `json:"locality"`
		Observation    *string `json:"observation"`
		ExpectedAmount *string `json:"expectedAmount"`
		FinalAmount    *string `json:"finalAmount"`
	}
	if !decodeClientJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	scheduledDate, err := time.Parse(time.RFC3339Nano, body.ScheduledDate)
	if err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "La fecha programada debe incluir zona horaria.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	service, err := h.service.Create(ctx, session.User, application.ServiceCreateInput{
		CompanyID: body.CompanyID, ClientID: body.ClientID, CategoryID: body.CategoryID,
		TechnicianID: body.TechnicianID, ScheduledDate: scheduledDate,
		Address: body.Address, Locality: body.Locality, Observation: body.Observation,
		ExpectedAmount: body.ExpectedAmount, FinalAmount: body.FinalAmount,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": service})
}

func (h *serviceHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		CompanyID      apiField[string] `json:"companyId"`
		ClientID       apiField[string] `json:"clientId"`
		CategoryID     apiField[string] `json:"categoryId"`
		TechnicianID   apiField[string] `json:"technicianId"`
		ScheduledDate  apiField[string] `json:"scheduledDate"`
		Address        apiField[string] `json:"address"`
		Locality       apiField[string] `json:"locality"`
		Observation    apiField[string] `json:"observation"`
		ExpectedAmount apiField[string] `json:"expectedAmount"`
		FinalAmount    apiField[string] `json:"finalAmount"`
	}
	if !decodeClientJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	var scheduled domain.Field[time.Time]
	if body.ScheduledDate.Set {
		scheduled.Set = true
		if body.ScheduledDate.Value != nil {
			parsed, err := time.Parse(time.RFC3339Nano, *body.ScheduledDate.Value)
			if err != nil {
				writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "La fecha programada debe incluir zona horaria.")
				return
			}
			parsed = parsed.UTC()
			scheduled.Value = &parsed
		}
	}
	patch := domain.ServicePatch{
		CompanyID:      domain.Field[string]{Set: body.CompanyID.Set, Value: body.CompanyID.Value},
		ClientID:       domain.Field[string]{Set: body.ClientID.Set, Value: body.ClientID.Value},
		CategoryID:     domain.Field[string]{Set: body.CategoryID.Set, Value: body.CategoryID.Value},
		TechnicianID:   domain.Field[string]{Set: body.TechnicianID.Set, Value: body.TechnicianID.Value},
		ScheduledDate:  scheduled,
		Address:        domain.Field[string]{Set: body.Address.Set, Value: body.Address.Value},
		Locality:       domain.Field[string]{Set: body.Locality.Set, Value: body.Locality.Value},
		Observation:    domain.Field[string]{Set: body.Observation.Set, Value: body.Observation.Value},
		ExpectedAmount: domain.Field[string]{Set: body.ExpectedAmount.Set, Value: body.ExpectedAmount.Value},
		FinalAmount:    domain.Field[string]{Set: body.FinalAmount.Set, Value: body.FinalAmount.Value},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	service, err := h.service.Update(ctx, session.User, r.PathValue("id"), patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": service})
}

func (h *serviceHandler) changeStatus(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if !decodeClientJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá el estado enviado.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	service, err := h.service.ChangeStatus(ctx, session.User, r.PathValue("id"), body.Status)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": service})
}

func (h *serviceHandler) finish(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		FinalAmount string  `json:"finalAmount"`
		Observation *string `json:"observation"`
	}
	if !decodeClientJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	service, err := h.service.Finish(ctx, session.User, r.PathValue("id"), domain.ServiceFinish{
		FinalAmount: body.FinalAmount, Observation: body.Observation,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": service})
}

func serviceListInput(r *http.Request) (application.ServiceListInput, error) {
	query := r.URL.Query()
	allowed := []string{
		"status", "technicianId", "clientId", "companyId", "categoryId",
		"dateFrom", "dateTo", "page", "pageSize",
	}
	if err := rejectUnknownQuery(query, allowed); err != nil {
		return application.ServiceListInput{}, err
	}
	page, pageSize, err := paginationQuery(query)
	if err != nil {
		return application.ServiceListInput{}, err
	}
	from, err := parseOptionalDate(query.Get("dateFrom"))
	if err != nil {
		return application.ServiceListInput{}, err
	}
	to, err := parseOptionalDate(query.Get("dateTo"))
	if err != nil {
		return application.ServiceListInput{}, err
	}
	return application.ServiceListInput{
		Status: query.Get("status"), TechnicianID: query.Get("technicianId"),
		ClientID: query.Get("clientId"), CompanyID: query.Get("companyId"),
		CategoryID: query.Get("categoryId"), DateFrom: from, DateTo: to,
		Page: page, PageSize: pageSize,
	}, nil
}

func paginationQuery(query url.Values) (int, int, error) {
	page, pageSize := 0, 0
	var err error
	if value := query.Get("page"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil {
			return 0, 0, err
		}
	}
	if value := query.Get("pageSize"); value != "" {
		pageSize, err = strconv.Atoi(value)
		if err != nil {
			return 0, 0, err
		}
	}
	return page, pageSize, nil
}

func rejectUnknownQuery(query url.Values, allowed []string) error {
	allowlist := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowlist[key] = struct{}{}
	}
	for key, values := range query {
		if _, ok := allowlist[key]; !ok || len(values) != 1 {
			return fmt.Errorf("invalid query parameter")
		}
	}
	return nil
}

func parseOptionalDate(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func (h *serviceHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrServiceForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para realizar esta operación.")
	case errors.Is(err, application.ErrInvalidService):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrServiceNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Servicio no encontrado.")
	case errors.Is(err, application.ErrServiceConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "El servicio cambió o entra en conflicto con una regla del negocio.")
	case errors.Is(err, application.ErrServiceRule):
		writeResourceError(h.logger, w, r, http.StatusUnprocessableEntity, "DOMAIN_RULE_VIOLATION", "La operación no cumple las reglas del servicio.")
	default:
		h.logger.Error("service operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ serviceUseCases = (*application.ServiceUseCases)(nil)
