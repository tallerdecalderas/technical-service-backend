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
)

type catalogUseCases interface {
	ListPrices(context.Context, domain.User, application.CatalogListInput) (domain.CatalogPage, error)
}

type catalogHandler struct {
	guard   resourceGuard
	service catalogUseCases
	logger  *slog.Logger
}

func NewCatalogHandler(auth authUseCases, service catalogUseCases, cfg config.AuthConfig, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &catalogHandler{guard: newResourceGuard(auth, cfg, logger), service: service, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/catalog/prices", handler.list)
	return mux
}

func (h *catalogHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), []string{"companyId", "search", "page", "pageSize", "includeInactive"}); err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
		return
	}
	includeInactive := false
	if values, present := r.URL.Query()["includeInactive"]; present {
		if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
			return
		}
		includeInactive = values[0] == "true"
	}
	page, pageSize, err := paginationQuery(r.URL.Query())
	if err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := h.service.ListPrices(ctx, session.User, application.CatalogListInput{
		CompanyID:       r.URL.Query().Get("companyId"),
		Search:          r.URL.Query().Get("search"),
		IncludeInactive: includeInactive,
		Page:            page,
		PageSize:        pageSize,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	includeCosts := strings.EqualFold(session.User.Role, "ADMIN")
	responses := make([]any, 0, len(result.Data))
	for _, price := range result.Data {
		responses = append(responses, catalogPriceResponse(price, includeCosts))
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": responses,
		"meta": map[string]any{
			"page": result.Page, "pageSize": result.PageSize, "total": result.Total,
			"version": result.Version, "updatedAt": result.UpdatedAt,
		},
	})
}

func catalogPriceResponse(price domain.CatalogPrice, includeCosts bool) any {
	response := map[string]any{
		"id": price.ID, "companyId": price.CompanyID, "companyName": price.CompanyName,
		"code": price.Code, "name": price.Name, "unit": price.Unit,
		"techPrice": price.TechPrice, "updatedAt": price.UpdatedAt,
	}
	if includeCosts {
		response["costPrice"] = price.CostPrice
		response["sellPrice"] = price.SellPrice
		response["costInitList"] = price.CostInitList
		response["marginPercent"] = price.MarginPercent
		response["isActive"] = price.IsActive
	}
	return response
}

func (h *catalogHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrCatalogForbidden):
		writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para consultar el catálogo.")
	case errors.Is(err, application.ErrInvalidCatalog):
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.")
	default:
		h.logger.Error("catalog operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.")
	}
}

var _ catalogUseCases = (*application.CatalogUseCases)(nil)
