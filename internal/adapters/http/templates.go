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

const maxTemplateRequestBytes = 1 << 20

const templateOperationTimeout = 5 * time.Second

type templateUseCases interface {
	List(context.Context, domain.User, string) (domain.TemplatePage, error)
	Create(context.Context, domain.User, domain.TemplateDraft) (string, error)
	Get(context.Context, domain.User, string) (*domain.Template, error)
	Update(context.Context, domain.User, string, domain.TemplatePatch) error
	Deactivate(context.Context, domain.User, string) error
}

type templateHandler struct {
	guard   resourceGuard
	service templateUseCases
	logger  *slog.Logger
}

func NewTemplateHandler(auth authUseCases, service templateUseCases, cfg config.AuthConfig, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &templateHandler{
		guard:   newResourceGuard(auth, cfg, logger),
		service: service,
		logger:  logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/templates", handler.list)
	mux.HandleFunc("POST /api/v1/templates", handler.create)
	mux.HandleFunc("GET /api/v1/templates/", handler.get)
	mux.HandleFunc("PATCH /api/v1/templates/", handler.update)
	return mux
}

type templateCreateRequest struct {
	Name     string `json:"name"`
	Content  string `json:"content,omitempty"`
}

type templateUpdateRequest struct {
	Name    *string `json:"name,omitempty"`
	Content *string `json:"content,omitempty"`
	IsActive *bool  `json:"isActive,omitempty"`
}

type templateResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Content   string    `json:"content,omitempty"`
	IsActive  bool      `json:"isActive"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
	CompanyID string    `json:"companyId,omitempty"`
	CompanyName string   `json:"companyName,omitempty"`
}

func (h *templateHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}

	companyID := strings.TrimPrefix(r.URL.Path, "/api/v1/templates")
	if companyID != "" && strings.HasPrefix(companyID, "/") {
		companyID = ""
	}
	companyID = strings.TrimSuffix(companyID, "/")

	ctx, cancel := context.WithTimeout(r.Context(), templateOperationTimeout)
	defer cancel()

	filter := domain.TemplateFilter{
		Actor:     session.User,
		CompanyID: companyID,
		Page:      1,
		PageSize:  25,
	}

	if r.URL.Query().Has("page") {
		if _, err := fmt.Sscanf(r.URL.Query().Get("page"), "%d", &filter.Page); err != nil {
			filter.Page = 1
		}
	}
	if r.URL.Query().Has("pageSize") {
		if _, err := fmt.Sscanf(r.URL.Query().Get("pageSize"), "%d", &filter.PageSize); err != nil {
			filter.PageSize = 25
		}
	}

	templates, err := h.service.List(ctx, session.User, companyID)
	if err != nil {
		if errors.Is(err, application.ErrTemplateForbidden) {
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para consultar plantillas.")
			return
		}
		h.logger.Error("template listing failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudieron cargar las plantillas.")
		return
	}

	response := make([]templateResponse, 0, len(templates.Data))
	for _, t := range templates.Data {
		response = append(response, templateResponse{
			ID:        t.ID,
			Name:      t.Name,
			Content:   t.Content,
			IsActive:  t.IsActive,
			CreatedAt: t.CreatedAt,
			UpdatedAt: t.UpdatedAt,
			CompanyID: t.CompanyID,
			CompanyName: t.CompanyName,
		})
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": response})
}

func (h *templateHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	var body templateCreateRequest
	if !decodeTemplateJSON(w, r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), templateOperationTimeout)
	defer cancel()

	id, err := h.service.Create(ctx, session.User, domain.TemplateDraft{
		Name:     body.Name,
		Content:  body.Content,
		CompanyID: session.User.CompanyIDs[0],
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrTemplateForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para crear plantillas.")
		case errors.Is(err, application.ErrInvalidTemplate):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "El nombre de la plantilla es requerido.")
		default:
			h.logger.Error("template creation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo crear la plantilla.")
		}
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": templateResponse{ID: id, Name: body.Name}})
}

func (h *templateHandler) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de plantilla requerido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), templateOperationTimeout)
	defer cancel()

	template, err := h.service.Get(ctx, session.User, id)
	if err != nil {
		if errors.Is(err, application.ErrTemplateForbidden) {
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para consultar esta plantilla.")
			return
		}
		if errors.Is(err, outbound.ErrRecordNotFound) {
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Plantilla no encontrada.")
			return
		}
		h.logger.Error("template get failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo obtener la plantilla.")
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": templateResponse{
		ID:        template.ID,
		Name:      template.Name,
		Content:   template.Content,
		IsActive:  template.IsActive,
		CreatedAt: template.CreatedAt,
		UpdatedAt: template.UpdatedAt,
		CompanyID: template.CompanyID,
	}})
}

func (h *templateHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de plantilla requerido.")
		return
	}

	var body templateUpdateRequest
	if !decodeTemplateJSON(w, r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), templateOperationTimeout)
	defer cancel()

	err := h.service.Update(ctx, session.User, id, domain.TemplatePatch{
		Name:    body.Name,
		Content: body.Content,
		IsActive: body.IsActive,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrTemplateForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para editar esta plantilla.")
		case errors.Is(err, application.ErrInvalidTemplate):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Datos de plantilla inválidos.")
		case errors.Is(err, outbound.ErrRecordNotFound):
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Plantilla no encontrada.")
		default:
			h.logger.Error("template update failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo actualizar la plantilla.")
		}
		return
	}
	var name string
	if body.Name != nil && *body.Name != "" {
		name = *body.Name
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": templateResponse{ID: id, Name: name}})
}

func (h *templateHandler) deactivate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de plantilla requerido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), templateOperationTimeout)
	defer cancel()

	if err := h.service.Deactivate(ctx, session.User, id); err != nil {
		switch {
		case errors.Is(err, application.ErrTemplateForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para desactivar esta plantilla.")
		case errors.Is(err, application.ErrInvalidTemplate):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de plantilla inválido.")
		case errors.Is(err, outbound.ErrRecordNotFound):
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Plantilla no encontrada.")
		default:
			h.logger.Error("template deactivation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo desactivar la plantilla.")
		}
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": map[string]string{"id": id, "status": "deactivated"}})
}

func decodeTemplateJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxTemplateRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}
