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
	"strings"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

const maxServicePartRequestBytes = 64 << 10

type servicePartUseCases interface {
	List(context.Context, domain.User, string) ([]domain.ServicePart, error)
	Create(context.Context, domain.User, application.ServicePartCreateInput) (domain.ServicePart, error)
	Update(context.Context, domain.User, string, string, domain.ServicePartPatch) (domain.ServicePart, error)
	Delete(context.Context, domain.User, string, string) error
}

type servicePartHandler struct {
	guard   resourceGuard
	service servicePartUseCases
	logger  *slog.Logger
}

func newServicePartHandler(auth authUseCases, service servicePartUseCases, cfg config.AuthConfig, logger *slog.Logger) *servicePartHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &servicePartHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
}

func registerServicePartRoutes(mux *http.ServeMux, auth authUseCases, service servicePartUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) {
	if service == nil {
		return
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := newServicePartHandler(auth, service, cfg, logger)
	mux.HandleFunc("GET /api/v1/services/{serviceId}/parts", handler.list)
	mux.HandleFunc("POST /api/v1/services/{serviceId}/parts", idempotentMutation(handler.guard, idempotency, handler.logger, handler.create))
	mux.HandleFunc("PATCH /api/v1/services/{serviceId}/parts/{partId}", idempotentMutation(handler.guard, idempotency, handler.logger, handler.update))
	mux.HandleFunc("DELETE /api/v1/services/{serviceId}/parts/{partId}", idempotentMutation(handler.guard, idempotency, handler.logger, handler.delete))
}

func (h *servicePartHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	parts, err := h.service.List(ctx, session.User, r.PathValue("serviceId"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	responses := make([]any, 0, len(parts))
	for _, part := range parts {
		responses = append(responses, servicePartResponse(part, strings.EqualFold(session.User.Role, "ADMIN")))
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": responses})
}

func (h *servicePartHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		InventoryItemID *string `json:"inventoryItemId"`
		Name            string  `json:"name"`
		Nota            *string `json:"nota"`
		Quantity        string  `json:"quantity"`
		UnitCost        string  `json:"unitCost"`
		UnitSalePrice   string  `json:"unitSalePrice"`
	}
	if !decodeServicePartJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	part, err := h.service.Create(ctx, session.User, application.ServicePartCreateInput{
		ServiceID: r.PathValue("serviceId"), InventoryItemID: body.InventoryItemID, Name: body.Name,
		Nota: body.Nota, Quantity: body.Quantity, UnitCost: body.UnitCost, UnitSalePrice: body.UnitSalePrice,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": servicePartResponse(part, true)})
}

func (h *servicePartHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Name          apiField[string] `json:"name"`
		Nota          apiField[string] `json:"nota"`
		Quantity      apiField[string] `json:"quantity"`
		UnitCost      apiField[string] `json:"unitCost"`
		UnitSalePrice apiField[string] `json:"unitSalePrice"`
	}
	if !decodeServicePartJSON(r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	part, err := h.service.Update(ctx, session.User, r.PathValue("serviceId"), r.PathValue("partId"), domain.ServicePartPatch{
		Name:          domain.Field[string]{Set: body.Name.Set, Value: body.Name.Value},
		Nota:          domain.Field[string]{Set: body.Nota.Set, Value: body.Nota.Value},
		Quantity:      domain.Field[string]{Set: body.Quantity.Set, Value: body.Quantity.Value},
		UnitCost:      domain.Field[string]{Set: body.UnitCost.Set, Value: body.UnitCost.Value},
		UnitSalePrice: domain.Field[string]{Set: body.UnitSalePrice.Set, Value: body.UnitSalePrice.Value},
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": servicePartResponse(part, true)})
}

func (h *servicePartHandler) delete(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := h.service.Delete(ctx, session.User, r.PathValue("serviceId"), r.PathValue("partId")); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeServicePartJSON(r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxServicePartRequestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func servicePartResponse(part domain.ServicePart, includeCosts bool) any {
	base := map[string]any{
		"id": part.ID, "serviceId": part.ServiceID, "name": part.Name, "nota": part.Nota,
		"quantity": part.Quantity, "unitSalePrice": part.UnitSalePrice, "totalSalePrice": part.TotalSalePrice,
		"createdAt": part.CreatedAt, "updatedAt": part.UpdatedAt,
	}
	if includeCosts {
		base["inventoryItemId"] = part.InventoryItemID
		base["providerName"] = part.ProviderName
		base["costUsd"] = part.CostUsd
		base["exchangeRate"] = part.ExchangeRate
		base["unitCost"] = part.UnitCost
		base["totalCost"] = part.TotalCost
	}
	return base
}

func (h *servicePartHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrServicePartForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para realizar esta operación.")
	case errors.Is(err, application.ErrInvalidServicePart):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.")
	case errors.Is(err, application.ErrServicePartNotFound):
		writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Repuesto o servicio no encontrado.")
	case errors.Is(err, application.ErrServicePartConflict):
		writeResourceError(h.logger, w, r, http.StatusConflict, "CONFLICT", "El repuesto entra en conflicto con una regla del negocio.")
	default:
		h.logger.Error("service part operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ servicePartUseCases = (*application.ServicePartUseCases)(nil)
