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
	"strconv"
	"time"

	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/domain"
	"technical-service-backend/internal/ports/outbound"
)

const clientOperationTimeout = 5 * time.Second

type clientUseCases interface {
	List(context.Context, domain.User, application.ClientListInput) (application.ClientListResult, error)
	Get(context.Context, domain.User, string) (domain.Client, error)
	Create(context.Context, domain.User, domain.ClientDraft) (domain.Client, error)
	Update(context.Context, domain.User, string, domain.ClientPatch) (domain.Client, error)
}

type clientHandler struct {
	auth    authUseCases
	service clientUseCases
	config  config.AuthConfig
	logger  *slog.Logger
}

func NewClientHandler(auth authUseCases, service clientUseCases, cfg config.AuthConfig, logger *slog.Logger, repositories ...outbound.IdempotencyRepository) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var idempotency outbound.IdempotencyRepository
	if len(repositories) > 0 {
		idempotency = repositories[0]
	}
	handler := &clientHandler{auth: auth, service: service, config: cfg, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/clients", handler.list)
	mux.HandleFunc("POST /api/v1/clients", idempotentMutation(
		newResourceGuard(auth, cfg, logger), idempotency, logger, handler.create,
	))
	mux.HandleFunc("GET /api/v1/clients/{id}", handler.get)
	mux.HandleFunc("PATCH /api/v1/clients/{id}", idempotentMutation(
		newResourceGuard(auth, cfg, logger), idempotency, logger, handler.update,
	))
	return mux
}

func (h *clientHandler) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	input, err := clientListInput(r)
	if err != nil {
		h.writeClientError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los filtros enviados.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clientOperationTimeout)
	defer cancel()
	result, err := h.service.List(ctx, session.User, input)
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"data": result.Data,
		"meta": map[string]int{"page": result.Page, "pageSize": result.PageSize, "total": result.Total},
	})
}

func (h *clientHandler) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clientOperationTimeout)
	defer cancel()
	client, err := h.service.Get(ctx, session.User, r.PathValue("id"))
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": client})
}

func (h *clientHandler) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Email    *string `json:"email"`
		Phone    string  `json:"phone"`
		Address  *string `json:"address"`
		Locality *string `json:"locality"`
	}
	if !decodeClientJSON(r, &body) {
		h.writeClientError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.", nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), clientOperationTimeout)
	defer cancel()
	client, err := h.service.Create(ctx, session.User, domain.ClientDraft{
		Name: body.Name, Email: body.Email, Phone: body.Phone, Address: body.Address, Locality: body.Locality,
	})
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusCreated, map[string]any{"data": client})
}

type optionalClientString struct {
	Set   bool
	Value *string
}

func (value *optionalClientString) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if string(raw) == "null" {
		value.Value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

func (h *clientHandler) update(w http.ResponseWriter, r *http.Request) {
	session, ok := h.mutationSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Name     optionalClientString `json:"name"`
		Email    optionalClientString `json:"email"`
		Phone    optionalClientString `json:"phone"`
		Address  optionalClientString `json:"address"`
		Locality optionalClientString `json:"locality"`
	}
	if !decodeClientJSON(r, &body) {
		h.writeClientError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.", nil)
		return
	}
	patch := domain.ClientPatch{
		Name:     domain.ClientField{Set: body.Name.Set, Value: body.Name.Value},
		Email:    domain.ClientField{Set: body.Email.Set, Value: body.Email.Value},
		Phone:    domain.ClientField{Set: body.Phone.Set, Value: body.Phone.Value},
		Address:  domain.ClientField{Set: body.Address.Set, Value: body.Address.Value},
		Locality: domain.ClientField{Set: body.Locality.Set, Value: body.Locality.Value},
	}
	ctx, cancel := context.WithTimeout(r.Context(), clientOperationTimeout)
	defer cancel()
	client, err := h.service.Update(ctx, session.User, r.PathValue("id"), patch)
	if err != nil {
		h.writeUseCaseError(w, r, err)
		return
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"data": client})
}

func (h *clientHandler) session(w http.ResponseWriter, r *http.Request) (application.Session, bool) {
	return newResourceGuard(h.auth, h.config, h.logger).session(w, r)
}

func (h *clientHandler) mutationSession(w http.ResponseWriter, r *http.Request) (application.Session, bool) {
	return newResourceGuard(h.auth, h.config, h.logger).mutationSession(w, r)
}

func clientListInput(r *http.Request) (application.ClientListInput, error) {
	query := r.URL.Query()
	for key, values := range query {
		if key != "search" && key != "page" && key != "pageSize" || len(values) != 1 {
			return application.ClientListInput{}, errors.New("invalid client list query")
		}
	}
	input := application.ClientListInput{Search: query.Get("search")}
	var err error
	if value := query.Get("page"); value != "" {
		input.Page, err = strconv.Atoi(value)
		if err != nil {
			return application.ClientListInput{}, err
		}
	}
	if value := query.Get("pageSize"); value != "" {
		input.PageSize, err = strconv.Atoi(value)
		if err != nil {
			return application.ClientListInput{}, err
		}
	}
	return input, nil
}

func decodeClientJSON(r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAuthRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func (h *clientHandler) writeUseCaseError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, application.ErrClientForbidden):
		h.writeClientError(w, r, http.StatusForbidden, "FORBIDDEN", "No tenés permiso para realizar esta operación.", nil)
	case errors.Is(err, application.ErrInvalidClientInput):
		h.writeClientError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "Revisá los datos enviados.", nil)
	case errors.Is(err, application.ErrClientNotFound):
		h.writeClientError(w, r, http.StatusNotFound, "NOT_FOUND", "Cliente no encontrado.", nil)
	case errors.Is(err, application.ErrClientConflict):
		h.writeClientError(w, r, http.StatusConflict, "CONFLICT", "El cliente ya existe o entra en conflicto con otro registro.", nil)
	default:
		h.logger.Error("client operation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
		h.writeClientError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "No se pudo completar la operación.", nil)
	}
}

func (h *clientHandler) writeClientError(w http.ResponseWriter, r *http.Request, status int, code, message string, details []any) {
	if details == nil {
		details = []any{}
	}
	writeJSON(h.logger, w, status, map[string]any{
		"error": map[string]any{
			"code": code, "message": message, "details": details, "requestId": RequestID(r.Context()),
		},
	})
}

var _ clientUseCases = (*application.ClientUseCases)(nil)
