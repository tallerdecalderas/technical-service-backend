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

type resourceGuard struct {
	auth   authUseCases
	config config.AuthConfig
	logger *slog.Logger
}

func newResourceGuard(auth authUseCases, cfg config.AuthConfig, logger *slog.Logger) resourceGuard {
	if logger == nil {
		logger = slog.Default()
	}
	return resourceGuard{auth: auth, config: cfg, logger: logger}
}

func (g resourceGuard) session(w http.ResponseWriter, r *http.Request) (application.Session, bool) {
	cookie, err := r.Cookie(g.config.SessionCookieName)
	if err != nil || cookie.Value == "" {
		writeResourceError(g.logger, w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "No autenticado.")
		return application.Session{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	session, err := g.auth.Session(ctx, cookie.Value)
	if err != nil {
		if errors.Is(err, application.ErrUnauthenticated) {
			writeResourceError(g.logger, w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "No autenticado.")
		} else {
			g.logger.Error("resource session validation failed", "request_id", RequestID(r.Context()), "error", fmt.Sprintf("%T", err))
			writeResourceError(g.logger, w, r, http.StatusServiceUnavailable, "AUTH_UNAVAILABLE", "No se pudo validar la sesión.")
		}
		return application.Session{}, false
	}
	session.User.CompanyIDs = activeCompanyIDs(session.Memberships)
	return session, true
}

func (g resourceGuard) mutationSession(w http.ResponseWriter, r *http.Request) (application.Session, bool) {
	origin := r.Header.Get("Origin")
	trusted := false
	for _, allowed := range g.config.TrustedOrigins {
		if origin != "" && origin == allowed {
			trusted = true
			break
		}
	}
	if !trusted {
		writeResourceError(g.logger, w, r, http.StatusForbidden, "ORIGIN_NOT_ALLOWED", "Origen no permitido.")
		return application.Session{}, false
	}
	session, ok := g.session(w, r)
	if !ok {
		return application.Session{}, false
	}
	csrfCookie, cookieErr := r.Cookie(g.config.CSRFCookieName)
	csrfHeader := r.Header.Get("X-CSRF-Token")
	if cookieErr != nil || csrfCookie.Value == "" || csrfCookie.Value != csrfHeader ||
		!g.auth.ValidateCSRF(session, csrfHeader) {
		writeResourceError(g.logger, w, r, http.StatusForbidden, "CSRF_INVALID", "Token CSRF inválido.")
		return application.Session{}, false
	}
	return session, true
}

func activeCompanyIDs(memberships []domain.CompanyMembership) []string {
	companyIDs := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		if membership.Active && membership.CompanyID != "" {
			companyIDs = append(companyIDs, membership.CompanyID)
		}
	}
	return companyIDs
}

func writeResourceError(logger *slog.Logger, w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(logger, w, status, map[string]any{
		"error": map[string]any{
			"code": code, "message": message, "details": []any{}, "requestId": RequestID(r.Context()),
		},
	})
}
