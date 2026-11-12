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

const authOperationTimeout = 5 * time.Second
const maxAuthRequestBytes = 1 << 20

type authUseCases interface {
	Login(context.Context, string, string) (application.Session, error)
	Session(context.Context, string) (application.Session, error)
	ValidateCSRF(application.Session, string) bool
	Logout(context.Context, string) error
	RequestPasswordReset(context.Context, string) error
	ResetPassword(context.Context, string, string) error
}

type authHandler struct {
	service authUseCases
	config  config.AuthConfig
	logger  *slog.Logger
}

func NewAuthHandler(service authUseCases, cfg config.AuthConfig, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &authHandler{service: service, config: cfg, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", handler.login)
	mux.HandleFunc("POST /api/v1/auth/logout", handler.logout)
	mux.HandleFunc("GET /api/v1/auth/session", handler.session)
	mux.HandleFunc("POST /api/v1/auth/password/forgot", handler.forgotPassword)
	mux.HandleFunc("POST /api/v1/auth/password/reset", handler.resetPassword)
	return mux
}

func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	if !h.trustedMutation(w, r) {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decodeAuthJSON(w, r, &body) || strings.TrimSpace(body.Email) == "" || body.Password == "" {
		writeJSON(h.logger, w, http.StatusBadRequest, map[string]any{"success": false, "error": "Ingresá un correo y una contraseña."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), authOperationTimeout)
	defer cancel()
	session, err := h.service.Login(ctx, body.Email, body.Password)
	if err != nil {
		h.writeAuthError(w, r, err, true)
		return
	}
	h.logAuthEvent(r, "login_succeeded", session.User.ID)
	h.setSessionCookies(w, session)
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"success": true, "data": userResponse(session.User, session.Memberships)})
}

func (h *authHandler) session(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(h.config.SessionCookieName)
	if err != nil || cookie.Value == "" {
		h.clearSessionCookies(w)
		writeJSON(h.logger, w, http.StatusUnauthorized, map[string]any{"success": false, "error": "No autenticado."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), authOperationTimeout)
	defer cancel()
	session, err := h.service.Session(ctx, cookie.Value)
	if err != nil {
		if errors.Is(err, application.ErrUnauthenticated) {
			h.clearSessionCookies(w)
		}
		h.writeAuthError(w, r, err, false)
		return
	}
	if csrfHeader := r.Header.Get("X-CSRF-Token"); csrfHeader != "" {
		csrfCookie, cookieErr := r.Cookie(h.config.CSRFCookieName)
		if cookieErr != nil || csrfCookie.Value != csrfHeader || !h.service.ValidateCSRF(session, csrfHeader) {
			writeJSON(h.logger, w, http.StatusForbidden, map[string]any{"success": false, "error": "Token CSRF inválido."})
			return
		}
	}
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"success": true, "data": userResponse(session.User, session.Memberships)})
}

func (h *authHandler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.trustedMutation(w, r) {
		return
	}
	userID := ""
	sessionCookie, err := r.Cookie(h.config.SessionCookieName)
	if err == nil && sessionCookie.Value != "" {
		ctx, cancel := context.WithTimeout(r.Context(), authOperationTimeout)
		defer cancel()
		session, sessionErr := h.service.Session(ctx, sessionCookie.Value)
		if sessionErr != nil && !errors.Is(sessionErr, application.ErrUnauthenticated) {
			h.writeAuthError(w, r, sessionErr, false)
			return
		}
		if sessionErr == nil {
			csrfCookie, cookieErr := r.Cookie(h.config.CSRFCookieName)
			csrfHeader := r.Header.Get("X-CSRF-Token")
			if cookieErr != nil || csrfCookie.Value == "" || csrfCookie.Value != csrfHeader ||
				!h.service.ValidateCSRF(session, csrfHeader) {
				writeJSON(h.logger, w, http.StatusForbidden, map[string]any{"success": false, "error": "Token CSRF inválido."})
				return
			}
			if err := h.service.Logout(ctx, sessionCookie.Value); err != nil {
				h.writeAuthError(w, r, err, false)
				return
			}
			userID = session.User.ID
		}
	}
	h.clearSessionCookies(w)
	h.logAuthEvent(r, "logout_succeeded", userID)
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"success": true})
}

func (h *authHandler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if !h.trustedMutation(w, r) {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if !decodeAuthJSON(w, r, &body) || strings.TrimSpace(body.Email) == "" {
		writeJSON(h.logger, w, http.StatusBadRequest, map[string]any{"success": false, "error": "Ingresá un correo válido."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), authOperationTimeout)
	defer cancel()
	if err := h.service.RequestPasswordReset(ctx, body.Email); err != nil {
		h.writeAuthError(w, r, err, false)
		return
	}
	h.logAuthEvent(r, "password_recovery_requested", "")
	writeJSON(h.logger, w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Si el correo está registrado, recibirás instrucciones para recuperar tu contraseña.",
	})
}

func (h *authHandler) resetPassword(w http.ResponseWriter, r *http.Request) {
	if !h.trustedMutation(w, r) {
		return
	}
	var body struct {
		AccessToken string `json:"accessToken"`
		Password    string `json:"password"`
	}
	if !decodeAuthJSON(w, r, &body) || body.AccessToken == "" || len(body.Password) < 8 {
		writeJSON(h.logger, w, http.StatusBadRequest, map[string]any{"success": false, "error": "El enlace o la nueva contraseña no son válidos."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), authOperationTimeout)
	defer cancel()
	if err := h.service.ResetPassword(ctx, body.AccessToken, body.Password); err != nil {
		h.writeAuthError(w, r, err, false)
		return
	}
	h.logAuthEvent(r, "password_reset_succeeded", "")
	writeJSON(h.logger, w, http.StatusOK, map[string]any{"success": true, "message": "Contraseña actualizada."})
}

func (h *authHandler) trustedMutation(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	for _, trusted := range h.config.TrustedOrigins {
		if origin != "" && origin == trusted {
			return true
		}
	}
	writeJSON(h.logger, w, http.StatusForbidden, map[string]any{"success": false, "error": "Origen no permitido."})
	return false
}

func (h *authHandler) logAuthEvent(r *http.Request, event, userID string) {
	attributes := []any{
		"event", event,
		"request_id", RequestID(r.Context()),
	}
	if userID != "" {
		attributes = append(attributes, "user_id", userID)
	}
	h.logger.Info("authentication audit event", attributes...)
}

func (h *authHandler) setSessionCookies(w http.ResponseWriter, session application.Session) {
	maxAge := int(h.config.SessionTTL.Seconds())
	w.Header().Add("Set-Cookie", (&http.Cookie{
		Name: h.config.SessionCookieName, Value: session.Token, Path: "/",
		HttpOnly: true, Secure: h.config.SecureCookies, SameSite: http.SameSiteLaxMode,
		MaxAge: maxAge, Expires: session.ExpiresAt,
	}).String())
	w.Header().Add("Set-Cookie", (&http.Cookie{
		Name: h.config.CSRFCookieName, Value: session.CSRFToken, Path: "/",
		HttpOnly: false, Secure: h.config.SecureCookies, SameSite: http.SameSiteLaxMode,
		MaxAge: maxAge, Expires: session.ExpiresAt,
	}).String())
}

func (h *authHandler) clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{h.config.SessionCookieName, h.config.CSRFCookieName} {
		w.Header().Add("Set-Cookie", (&http.Cookie{
			Name: name, Value: "", Path: "/", HttpOnly: name == h.config.SessionCookieName,
			Secure: h.config.SecureCookies, SameSite: http.SameSiteLaxMode,
			MaxAge: -1, Expires: time.Unix(1, 0),
		}).String())
	}
}

func (h *authHandler) writeAuthError(w http.ResponseWriter, r *http.Request, err error, login bool) {
	status := http.StatusInternalServerError
	message := "No se pudo completar la operación."
	switch {
	case errors.Is(err, outbound.ErrInvalidCredentials), errors.Is(err, application.ErrUnauthenticated):
		status = http.StatusUnauthorized
		message = "Credenciales inválidas o cuenta no habilitada."
	case errors.Is(err, application.ErrInvalidResetRequest):
		status = http.StatusBadRequest
		message = "El enlace o la nueva contraseña no son válidos."
	case errors.Is(err, outbound.ErrIdentityProviderUnavailable):
		status = http.StatusBadGateway
		message = "El servicio de autenticación no está disponible."
	case errors.Is(err, outbound.ErrRateLimited):
		status = http.StatusTooManyRequests
		message = "Demasiados intentos. Esperá un momento e intentá nuevamente."
	case errors.Is(err, application.ErrAuthStoreUnavailable):
		status = http.StatusServiceUnavailable
		message = "No se pudo validar la sesión."
	}
	if login && status == http.StatusInternalServerError {
		message = "No se pudo iniciar sesión."
	}
	h.logger.Error("authentication audit event",
		"event", "operation_failed",
		"request_id", RequestID(r.Context()),
		"path", r.URL.Path,
		"status", status,
		"error", fmt.Sprintf("%T", err),
	)
	writeJSON(h.logger, w, status, map[string]any{"success": false, "error": message})
}

func decodeAuthJSON(w http.ResponseWriter, r *http.Request, target any) bool {
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

func userResponse(user domain.User, memberships []domain.CompanyMembership) map[string]any {
	companies := make([]map[string]any, 0, len(memberships))
	for _, membership := range memberships {
		if membership.Active {
			companies = append(companies, map[string]any{"id": membership.CompanyID})
		}
	}
	return map[string]any{
		"id": user.ID, "email": user.Email, "name": user.Name,
		"role": user.Role, "avatar": user.Avatar, "companies": companies,
	}
}
