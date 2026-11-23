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

const maxUserRequestBytes = 1 << 20

const userOperationTimeout = 5 * time.Second

type userUseCases interface {
	List(context.Context, domain.User) ([]domain.UserSummary, error)
	Create(context.Context, domain.User, domain.UserCreateInput) (string, error)
	Get(context.Context, domain.User, string) (*domain.User, error)
	Update(context.Context, domain.User, string, domain.UserUpdateInput) error
	Deactivate(context.Context, domain.User, string) error
}

type userHandler struct {
	guard   resourceGuard
	service userUseCases
	logger  *slog.Logger
}

func NewUserHandler(auth authUseCases, service userUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &userHandler{
		guard:   newResourceGuard(auth, cfg, logger),
		service: service,
		logger:  logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/users", handler.list)
	mux.HandleFunc("POST /api/v1/users", idempotentMutation(handler.guard, getUserIdempotency(repositories), logger, handler.create))
	mux.HandleFunc("PATCH /api/v1/users/", idempotentMutation(handler.guard, getUserIdempotency(repositories), logger, handler.update))
	mux.HandleFunc("DELETE /api/v1/users/", idempotentMutation(handler.guard, getUserIdempotency(repositories), logger, handler.deactivate))
	return mux
}

func getUserIdempotency(repositories []outbound.IdempotencyRepository) outbound.IdempotencyRepository {
	if len(repositories) > 0 {
		return repositories[0]
	}
	return nil
}

type userCreateRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
	Phone    string `json:"phone,omitempty"`
	DNI      string `json:"dni,omitempty"`
}

type userUpdateRequest struct {
	Name     *string `json:"name,omitempty"`
	Email    *string `json:"email,omitempty"`
	Role     *string `json:"role,omitempty"`
	Phone    *string `json:"phone,omitempty"`
	DNI      *string `json:"dni,omitempty"`
	IsActive *bool   `json:"isActive,omitempty"`
}

type userHTTPResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	IsActive  bool      `json:"isActive"`
	Phone     string    `json:"phone,omitempty"`
	DNI       string    `json:"dni,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

func (h *userHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.session(w, r)
	if !ok {
		return
	}
	if err := rejectUnknownQuery(r.URL.Query(), nil); err != nil {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "No se admiten filtros para esta consulta.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), userOperationTimeout)
	defer cancel()
	users, err := h.service.List(ctx, session.User)
	if err != nil {
		if errors.Is(err, application.ErrUserForbidden) {
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para consultar usuarios.")
			return
		}
		h.logger.Error("user listing failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudieron cargar los usuarios.")
		return
	}

	response := make([]userHTTPResponse, 0, len(users))
	for _, u := range users {
		response = append(response, userHTTPResponse{
			ID:        u.ID,
			Name:      u.Name,
			Email:     u.Email,
			Role:      u.Role,
			IsActive:  u.IsActive,
			Phone:     u.Phone,
			DNI:       u.DNI,
			CreatedAt: u.CreatedAt,
			UpdatedAt: u.UpdatedAt,
		})
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": response})
}

func (h *userHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	var body userCreateRequest
	if !decodeUserJSON(w, r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), userOperationTimeout)
	defer cancel()

	_, err := h.service.Create(ctx, session.User, domain.UserCreateInput{
		Name:     body.Name,
		Email:    body.Email,
		Password: body.Password,
		Role:     body.Role,
		Phone:    body.Phone,
		DNI:      body.DNI,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrUserForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para crear usuarios.")
		case errors.Is(err, application.ErrInvalidUser):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Los datos del usuario son inválidos.")
		default:
			h.logger.Error("user creation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo crear el usuario.")
		}
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": userHTTPResponse{ID: body.Name, Name: body.Name}})
}

func (h *userHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de usuario requerido.")
		return
	}

	var body userUpdateRequest
	if !decodeUserJSON(w, r, &body) {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Cuerpo de solicitud inválido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), userOperationTimeout)
	defer cancel()

	err := h.service.Update(ctx, session.User, id, domain.UserUpdateInput{
		Name:     body.Name,
		Email:    body.Email,
		Role:     body.Role,
		Phone:    body.Phone,
		DNI:      body.DNI,
		IsActive: body.IsActive,
	})
	if err != nil {
		switch {
		case errors.Is(err, application.ErrUserForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para editar este usuario.")
		case errors.Is(err, application.ErrInvalidUser):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Datos de usuario inválidos.")
		case errors.Is(err, outbound.ErrRecordNotFound):
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Usuario no encontrado.")
		default:
			h.logger.Error("user update failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo actualizar el usuario.")
		}
		return
	}
	var name string
	if body.Name != nil && *body.Name != "" {
		name = *body.Name
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": userHTTPResponse{ID: id, Name: name}})
}

func (h *userHandler) deactivate(w http.ResponseWriter, r *http.Request) {
	session, ok := h.guard.mutationSession(w, r)
	if !ok {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de usuario requerido.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), userOperationTimeout)
	defer cancel()

	if err := h.service.Deactivate(ctx, session.User, id); err != nil {
		switch {
		case errors.Is(err, application.ErrUserForbidden):
			writeResourceError(h.logger, w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para desactivar este usuario.")
		case errors.Is(err, application.ErrInvalidUser):
			writeResourceError(h.logger, w, r, http.StatusBadRequest, "VALIDATION_ERROR", "ID de usuario inválido.")
		case errors.Is(err, outbound.ErrRecordNotFound):
			writeResourceError(h.logger, w, r, http.StatusNotFound, "NOT_FOUND", "Usuario no encontrado.")
		default:
			h.logger.Error("user deactivation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(h.logger, w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo desactivar el usuario.")
		}
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": map[string]string{"id": id, "status": "deactivated"}})
}

func decodeUserJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxUserRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}