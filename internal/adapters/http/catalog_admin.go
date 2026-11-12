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

type catalogAdminUseCases interface {
	ListProviders(context.Context, domain.User, application.CatalogProviderListInput) (domain.CatalogProviderPage, error)
	CreateProvider(context.Context, domain.User, string, string) (domain.CatalogProvider, error)
	UpdateProvider(context.Context, domain.User, string, domain.CatalogProviderPatch) (domain.CatalogProvider, error)
	ListOffers(context.Context, domain.User, application.CatalogOfferListInput) (domain.CatalogOfferPage, error)
	CreateOffer(context.Context, domain.User, string, string, string, string) (domain.CatalogOffer, error)
	UpdateOffer(context.Context, domain.User, string, domain.CatalogOfferPatch) (domain.CatalogOffer, error)
	LatestExchangeRate(context.Context, domain.User) (domain.ExchangeRate, error)
	UpsertExchangeRate(context.Context, domain.User, string, string) (domain.ExchangeRate, error)
	UpdateItem(context.Context, domain.User, string, domain.CatalogItemPatch) (domain.CatalogItemPrice, error)
}

type catalogAdminHandler struct {
	guard   resourceGuard
	service catalogAdminUseCases
	logger  *slog.Logger
}

func NewCatalogAdminHandler(auth authUseCases, service catalogAdminUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &catalogAdminHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/catalog/providers", handler.listProviders)
	mux.HandleFunc("POST /api/v1/catalog/providers", idempotentMutation(handler.guard, idempotency, logger, handler.createProvider))
	mux.HandleFunc("PATCH /api/v1/catalog/providers/{id}", idempotentMutation(handler.guard, idempotency, logger, handler.updateProvider))
	mux.HandleFunc("GET /api/v1/catalog/offers", handler.listOffers)
	mux.HandleFunc("POST /api/v1/catalog/offers", idempotentMutation(handler.guard, idempotency, logger, handler.createOffer))
	mux.HandleFunc("PATCH /api/v1/catalog/offers/{id}", idempotentMutation(handler.guard, idempotency, logger, handler.updateOffer))
	mux.HandleFunc("GET /api/v1/catalog/exchange-rates/latest", handler.latestRate)
	mux.HandleFunc("POST /api/v1/catalog/exchange-rates", idempotentMutation(handler.guard, idempotency, logger, handler.upsertRate))
	mux.HandleFunc("PATCH /api/v1/catalog/items/{id}", idempotentMutation(handler.guard, idempotency, logger, handler.updateItem))
	return mux
}

func (h *catalogAdminHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), []string{"companyId", "search", "page", "pageSize"}); err != nil {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	page, pageSize, err := paginationQuery(r.URL.Query())
	if err != nil {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := h.service.ListProviders(ctx, session.User, application.CatalogProviderListInput{
		CompanyID: r.URL.Query().Get("companyId"), Search: r.URL.Query().Get("search"),
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": result.Data,
		"meta": map[string]int{"page": result.Page, "pageSize": result.PageSize, "total": result.Total},
	})
}

func (h *catalogAdminHandler) createProvider(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		CompanyID string `json:"companyId"`
		Name      string `json:"name"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	provider, err := h.service.CreateProvider(ctx, session.User, body.CompanyID, body.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": provider})
}

func (h *catalogAdminHandler) updateProvider(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Name     apiField[string] `json:"name"`
		IsActive apiField[bool]   `json:"isActive"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	provider, err := h.service.UpdateProvider(ctx, session.User, r.PathValue("id"), domain.CatalogProviderPatch{
		Name:     domain.Field[string]{Set: body.Name.Set, Value: body.Name.Value},
		IsActive: domain.Field[bool]{Set: body.IsActive.Set, Value: body.IsActive.Value},
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": provider})
}

func (h *catalogAdminHandler) listOffers(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), []string{"companyId", "inventoryItemId", "page", "pageSize"}); err != nil {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	page, pageSize, err := paginationQuery(r.URL.Query())
	if err != nil {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := h.service.ListOffers(ctx, session.User, application.CatalogOfferListInput{
		CompanyID: r.URL.Query().Get("companyId"), InventoryItemID: r.URL.Query().Get("inventoryItemId"),
		Page: page, PageSize: pageSize,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": result.Data,
		"meta": map[string]int{"page": result.Page, "pageSize": result.PageSize, "total": result.Total},
	})
}

func (h *catalogAdminHandler) createOffer(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		CompanyID       string `json:"companyId"`
		InventoryItemID string `json:"inventoryItemId"`
		ProviderID      string `json:"providerId"`
		CostUsd         string `json:"costUsd"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	offer, err := h.service.CreateOffer(ctx, session.User, body.CompanyID, body.InventoryItemID, body.ProviderID, body.CostUsd)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": offer})
}

func (h *catalogAdminHandler) updateOffer(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		CostUsd  apiField[string] `json:"costUsd"`
		IsActive apiField[bool]   `json:"isActive"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	offer, err := h.service.UpdateOffer(ctx, session.User, r.PathValue("id"), domain.CatalogOfferPatch{
		CostUsd:  domain.Field[string]{Set: body.CostUsd.Set, Value: body.CostUsd.Value},
		IsActive: domain.Field[bool]{Set: body.IsActive.Set, Value: body.IsActive.Value},
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": offer})
}

func (h *catalogAdminHandler) latestRate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), nil); err != nil {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rate, err := h.service.LatestExchangeRate(ctx, session.User)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": rate})
}

func (h *catalogAdminHandler) upsertRate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		RateDate string `json:"rateDate"`
		Rate     string `json:"rate"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rate, err := h.service.UpsertExchangeRate(ctx, session.User, body.RateDate, body.Rate)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": rate})
}

func (h *catalogAdminHandler) updateItem(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		MarginPercent apiField[string] `json:"marginPercent"`
		TechPrice     apiField[string] `json:"techPrice"`
		IsActive      apiField[bool]   `json:"isActive"`
	}
	if !decodePaymentJSON(r, &body) {
		h.writeError(w, r, application.ErrInvalidCatalogAdmin)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	item, err := h.service.UpdateItem(ctx, session.User, r.PathValue("id"), domain.CatalogItemPatch{
		MarginPercent: domain.Field[string]{Set: body.MarginPercent.Set, Value: body.MarginPercent.Value},
		TechPrice:     domain.Field[string]{Set: body.TechPrice.Set, Value: body.TechPrice.Value},
		IsActive:      domain.Field[bool]{Set: body.IsActive.Set, Value: body.IsActive.Value},
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": item})
}

func (h *catalogAdminHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrCatalogAdminForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para administrar el catálogo.")
	case errors.Is(err, application.ErrInvalidCatalogAdmin):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrCatalogAdminNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Recurso del catálogo no encontrado.")
	case errors.Is(err, application.ErrCatalogAdminConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "La operación entra en conflicto con un registro existente.")
	case errors.Is(err, application.ErrCatalogAdminRule), errors.Is(err, outbound.ErrRuleViolation):
		writeResourceError(h.logger, w, r, http.StatusUnprocessableEntity, "DOMAIN_RULE_VIOLATION", "La operación requiere datos o configuración faltante (cotización, proveedor o migración).")
	default:
		h.logger.Error("catalog administration failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ catalogAdminUseCases = (*application.CatalogAdminUseCases)(nil)
