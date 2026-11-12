package httpadapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

type settlementUseCases interface {
	List(context.Context, domain.User, application.SettlementListInput) (domain.SettlementPage, error)
	Get(context.Context, domain.User, string) (domain.Settlement, error)
	Generate(context.Context, domain.User, application.SettlementGenerateInput) (domain.Settlement, error)
	Update(context.Context, domain.User, string, domain.SettlementPatch) (domain.Settlement, error)
	Liquidate(context.Context, domain.User, string) (domain.Settlement, error)
}

type settlementHandler struct {
	guard   resourceGuard
	service settlementUseCases
	logger  *slog.Logger
}

type settlementAdjustmentBody struct {
	Kind    string `json:"kind"`
	Concept string `json:"concept"`
	Amount  string `json:"amount"`
}

func NewSettlementHandler(auth authUseCases, service settlementUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &settlementHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/settlements", handler.list)
	mux.HandleFunc("POST /api/v1/settlements", idempotentMutation(handler.guard, idempotency, logger, handler.create))
	mux.HandleFunc("GET /api/v1/settlements/{id}", handler.get)
	mux.HandleFunc("PATCH /api/v1/settlements/{id}", idempotentMutation(handler.guard, idempotency, logger, handler.update))
	mux.HandleFunc("POST /api/v1/settlements/{id}/liquidate", idempotentMutation(handler.guard, idempotency, logger, handler.liquidate))
	return mux
}

func (h *settlementHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), []string{
		"technicianId", "companyId", "status", "dateFrom", "dateTo", "page", "pageSize",
	}); err != nil {
		h.writeError(w, r, application.ErrInvalidSettlement)
		return
	}
	page, pageSize, err := paginationQuery(r.URL.Query())
	if err != nil {
		h.writeError(w, r, application.ErrInvalidSettlement)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := h.service.List(ctx, session.User, application.SettlementListInput{
		TechnicianID: r.URL.Query().Get("technicianId"), CompanyID: r.URL.Query().Get("companyId"),
		Status: r.URL.Query().Get("status"), DateFrom: r.URL.Query().Get("dateFrom"),
		DateTo: r.URL.Query().Get("dateTo"), Page: page, PageSize: pageSize,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	for index := range result.Data {
		result.Data[index].Items = []domain.SettlementItem{}
		result.Data[index].Parts = []domain.SettlementPart{}
		result.Data[index].Expenses = []domain.SettlementExpense{}
		result.Data[index].Adjustments = []domain.SettlementAdjustment{}
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": result.Data,
		"meta": map[string]int{"page": result.Page, "pageSize": result.PageSize, "total": result.Total},
	})
}

func (h *settlementHandler) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), nil); err != nil {
		h.writeError(w, r, application.ErrInvalidSettlement)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	settlement, err := h.service.Get(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": settlement})
}

func (h *settlementHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		TechnicianID string                     `json:"technicianId"`
		CompanyID    string                     `json:"companyId"`
		StartDate    string                     `json:"startDate"`
		EndDate      string                     `json:"endDate"`
		Label        string                     `json:"label"`
		CompanyRate  string                     `json:"companyRate"`
		Insurance    []settlementAdjustmentBody `json:"insurance"`
		Adjustments  []settlementAdjustmentBody `json:"adjustments"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidSettlement)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	settlement, err := h.service.Generate(ctx, session.User, application.SettlementGenerateInput{
		TechnicianID: body.TechnicianID, CompanyID: body.CompanyID,
		StartDate: body.StartDate, EndDate: body.EndDate, Label: body.Label, CompanyRate: body.CompanyRate,
		Insurance: toSettlementAdjustmentInputs(body.Insurance), Adjustments: toSettlementAdjustmentInputs(body.Adjustments),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": settlement})
}

func (h *settlementHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Label       apiField[string]            `json:"label"`
		CompanyRate apiField[string]            `json:"companyRate"`
		Insurance   *[]settlementAdjustmentBody `json:"insurance"`
		Adjustments *[]settlementAdjustmentBody `json:"adjustments"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidSettlement)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var insurance *[]domain.SettlementAdjustmentDraft
	if body.Insurance != nil {
		values := toSettlementAdjustmentDrafts(*body.Insurance)
		insurance = &values
	}
	var adjustments *[]domain.SettlementAdjustmentDraft
	if body.Adjustments != nil {
		values := toSettlementAdjustmentDrafts(*body.Adjustments)
		adjustments = &values
	}
	settlement, err := h.service.Update(ctx, session.User, r.PathValue("id"), domain.SettlementPatch{
		Label:       domain.Field[string]{Set: body.Label.Set, Value: body.Label.Value},
		CompanyRate: domain.Field[string]{Set: body.CompanyRate.Set, Value: body.CompanyRate.Value},
		Insurance:   insurance, Adjustments: adjustments,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": settlement})
}

func (h *settlementHandler) liquidate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), nil); err != nil {
		h.writeError(w, r, application.ErrInvalidSettlement)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	settlement, err := h.service.Liquidate(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": settlement})
}

func toSettlementAdjustmentInputs(values []settlementAdjustmentBody) []application.SettlementAdjustmentInput {
	result := make([]application.SettlementAdjustmentInput, 0, len(values))
	for _, value := range values {
		result = append(result, application.SettlementAdjustmentInput{Kind: value.Kind, Concept: value.Concept, Amount: value.Amount})
	}
	return result
}

func toSettlementAdjustmentDrafts(values []settlementAdjustmentBody) []domain.SettlementAdjustmentDraft {
	result := make([]domain.SettlementAdjustmentDraft, 0, len(values))
	for _, value := range values {
		result = append(result, domain.SettlementAdjustmentDraft{Kind: value.Kind, Concept: value.Concept, Amount: value.Amount})
	}
	return result
}

func (h *settlementHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrSettlementForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para operar esta rendición.")
	case errors.Is(err, application.ErrInvalidSettlement):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrSettlementNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Rendición no encontrada.")
	case errors.Is(err, application.ErrSettlementConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "La rendición entra en conflicto con otra existente o ya fue liquidada.")
	case errors.Is(err, application.ErrSettlementRule), errors.Is(err, outbound.ErrRuleViolation):
		writeResourceError(h.logger, w, r, http.StatusUnprocessableEntity, "DOMAIN_RULE_VIOLATION", "La rendición no cumple las reglas del período, del técnico o de la cotización.")
	default:
		h.logger.Error("settlement operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ settlementUseCases = (*application.SettlementUseCases)(nil)
