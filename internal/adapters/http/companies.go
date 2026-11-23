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
)

const companyOperationTimeout = 5 * time.Second

type companyUseCases interface {
	List(context.Context, domain.User) ([]domain.CompanySummary, error)
}

type companyHandler struct {
	guard   resourceGuard
	service companyUseCases
	logger  *slog.Logger
}

func NewCompanyHandler(auth authUseCases, service companyUseCases, cfg config.AuthConfig, logger *slog.Logger) http.Handler {
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
	return mux
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

	response := make([]companySummaryResponse, 0, len(companies))
	for _, company := range companies {
		response = append(response, companySummaryResponse{ID: company.ID, Name: company.Name})
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": response})
}

type companySummaryResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
