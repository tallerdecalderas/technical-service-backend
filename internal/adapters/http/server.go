package httpadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"technical-service-backend/internal/config"
	"technical-service-backend/internal/ports/outbound"
)

const readinessTimeout = 2 * time.Second
const shutdownTimeout = 10 * time.Second

type HealthChecker interface {
	Ping(context.Context) error
}

func NewHandler(health HealthChecker, allowedOrigins []string, logger *slog.Logger) http.Handler {
	return newHandler(health, allowedOrigins, nil, nil, nil, nil, config.AuthConfig{}, logger)
}

func NewHandlerWithAuth(
	health HealthChecker,
	allowedOrigins []string,
	authConfig config.AuthConfig,
	authService authUseCases,
	logger *slog.Logger,
) http.Handler {
	return newHandler(health, allowedOrigins, authService, nil, nil, nil, authConfig, logger)
}

func NewHandlerWithBusiness(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandler(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, authConfig, logger, idempotency...,
	)
}

func NewHandlerWithPayments(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	paymentService paymentUseCases,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandlerWithEstimates(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, paymentService, nil, nil, nil, nil, nil, nil, nil, nil,
		config.StorageConfig{}, nil, authConfig, logger, idempotency...,
	)
}

func NewHandlerWithEstimates(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	paymentService paymentUseCases,
	estimateService estimateUseCases,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandlerWithEstimates(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, paymentService, estimateService, nil, nil, nil, nil, nil, nil, nil,
		config.StorageConfig{}, nil, authConfig, logger, idempotency...,
	)
}

func NewHandlerWithServiceParts(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	paymentService paymentUseCases,
	estimateService estimateUseCases,
	servicePartService servicePartUseCases,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandlerWithEstimates(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, paymentService, estimateService, servicePartService,
		nil, nil, nil, nil, nil, nil,
		config.StorageConfig{}, nil, authConfig, logger, idempotency...,
	)
}

func NewHandlerWithPhase6(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	paymentService paymentUseCases,
	estimateService estimateUseCases,
	servicePartService servicePartUseCases,
	expenseService expenseUseCases,
	catalogService catalogUseCases,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandlerWithEstimates(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, paymentService, estimateService, servicePartService,
		expenseService, catalogService, nil, nil, nil, nil,
		config.StorageConfig{}, nil, authConfig, logger, idempotency...,
	)
}

func NewHandlerWithPhase6Complete(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	paymentService paymentUseCases,
	estimateService estimateUseCases,
	servicePartService servicePartUseCases,
	expenseService expenseUseCases,
	catalogService catalogUseCases,
	catalogAdminService catalogAdminUseCases,
	catalogImportService catalogImportUseCases,
	settlementService settlementUseCases,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandlerWithEstimates(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, paymentService, estimateService, servicePartService,
		expenseService, catalogService, catalogAdminService, catalogImportService, settlementService,
		nil, config.StorageConfig{}, nil, authConfig, logger, idempotency...,
	)
}

func NewHandlerWithPhase7(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	paymentService paymentUseCases,
	estimateService estimateUseCases,
	servicePartService servicePartUseCases,
	expenseService expenseUseCases,
	catalogService catalogUseCases,
	catalogAdminService catalogAdminUseCases,
	catalogImportService catalogImportUseCases,
	settlementService settlementUseCases,
	storageService storageUseCases,
	storageCfg config.StorageConfig,
	fileStore outbound.FileStore,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandlerWithEstimates(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, paymentService, estimateService, servicePartService,
		expenseService, catalogService, catalogAdminService, catalogImportService, settlementService,
		storageService, storageCfg, fileStore, authConfig, logger, idempotency...,
	)
}

func newHandler(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	return newHandlerWithEstimates(
		health, allowedOrigins, authService, clientService, technicianService,
		serviceService, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, config.StorageConfig{}, nil, authConfig, logger, idempotency...,
	)
}

func newHandlerWithEstimates(
	health HealthChecker,
	allowedOrigins []string,
	authService authUseCases,
	clientService clientUseCases,
	technicianService technicianUseCases,
	serviceService serviceUseCases,
	paymentService paymentUseCases,
	estimateService estimateUseCases,
	servicePartService servicePartUseCases,
	expenseService expenseUseCases,
	catalogService catalogUseCases,
	catalogAdminService catalogAdminUseCases,
	catalogImportService catalogImportUseCases,
	settlementService settlementUseCases,
	storageService storageUseCases,
	storageCfg config.StorageConfig,
	fileStore outbound.FileStore,
	authConfig config.AuthConfig,
	logger *slog.Logger,
	idempotency ...outbound.IdempotencyRepository,
) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origins[origin] = struct{}{}
	}
	var idempotencyRepository outbound.IdempotencyRepository
	if len(idempotency) > 0 {
		idempotencyRepository = idempotency[0]
	}

	mux := http.NewServeMux()
	if authService != nil {
		mux.Handle("/api/v1/auth/", NewAuthHandler(authService, authConfig, logger))
	}
	if clientService != nil {
		handler := NewClientHandler(authService, clientService, authConfig, logger, idempotencyRepository)
		mux.Handle("/api/v1/clients", handler)
		mux.Handle("/api/v1/clients/", handler)
	}
	if technicianService != nil {
		handler := NewTechnicianHandler(authService, technicianService, authConfig, logger, idempotencyRepository)
		mux.Handle("/api/v1/technicians", handler)
		mux.Handle("/api/v1/technicians/", handler)
	}
	if serviceService != nil {
		handler := NewServiceHandlerWithPartsAndExpenses(
			authService, serviceService, servicePartService, expenseService, authConfig, logger, idempotencyRepository,
		)
		mux.Handle("/api/v1/services", handler)
		mux.Handle("/api/v1/services/", handler)
		mux.Handle("/api/v1/schedule", handler)
	}
	if catalogService != nil {
		mux.Handle("/api/v1/catalog/prices", NewCatalogHandler(authService, catalogService, authConfig, logger))
	}
	if catalogAdminService != nil {
		handler := NewCatalogAdminHandler(authService, catalogAdminService, authConfig, logger, idempotencyRepository)
		mux.Handle("/api/v1/catalog/providers", handler)
		mux.Handle("/api/v1/catalog/providers/", handler)
		mux.Handle("/api/v1/catalog/offers", handler)
		mux.Handle("/api/v1/catalog/offers/", handler)
		mux.Handle("/api/v1/catalog/exchange-rates", handler)
		mux.Handle("/api/v1/catalog/exchange-rates/", handler)
		mux.Handle("/api/v1/catalog/items/", handler)
	}
	if catalogImportService != nil {
		importHandler := NewCatalogImportHandler(authService, catalogImportService, authConfig, logger, idempotencyRepository)
		mux.Handle("/api/v1/catalog/import", importHandler)
		mux.Handle("/api/v1/integrations/google-sheets/catalog-import", importHandler)
	}
	if storageService != nil {
		registerStorageRoutes(mux, authService, storageService, authConfig, storageCfg, logger, fileStore, idempotencyRepository)
	}
	if settlementService != nil {
		handler := NewSettlementHandler(authService, settlementService, authConfig, logger, idempotencyRepository)
		mux.Handle("/api/v1/settlements", handler)
		mux.Handle("/api/v1/settlements/", handler)
	}
	if paymentService != nil {
		handler := NewPaymentHandler(authService, paymentService, authConfig, logger, idempotencyRepository)
		mux.Handle("/api/v1/payments", handler)
		mux.Handle("/api/v1/payments/", handler)
	}
	if estimateService != nil {
		handler := NewEstimateHandler(authService, estimateService, authConfig, logger, idempotencyRepository)
		mux.Handle("/api/v1/estimates", handler)
		mux.Handle("/api/v1/estimates/", handler)
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(logger, w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		if health == nil {
			logger.Error("readiness check failed", "request_id", RequestID(r.Context()), "reason", "database health checker is unavailable")
			writeJSON(logger, w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		if err := health.Ping(ctx); err != nil {
			logger.Error("readiness check failed", "request_id", RequestID(r.Context()), "error", err)
			writeJSON(logger, w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(logger, w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return requestIDMiddleware(logger,
		requestLoggingMiddleware(logger,
			corsMiddleware(origins, mux),
		),
	)
}

func NewServer(port int, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + strconv.Itoa(port),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func Serve(ctx context.Context, server *http.Server, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen for HTTP requests: %w", err)
	}

	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.Serve(listener)
	}()
	logger.Info("HTTP server listening", "address", listener.Addr().String())

	select {
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("HTTP server shutdown failed: %w", err)
		}
		if err := <-serverErrors; !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("HTTP server stopped unexpectedly: %w", err)
		}
		logger.Info("HTTP server stopped")
		return nil
	}
}

func writeJSON(logger *slog.Logger, w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		logger.Error("failed to write HTTP response", "error", err)
	}
}

type requestIDContextKey struct{}

func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

func newRequestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80

	var encoded [36]byte
	hex.Encode(encoded[0:8], id[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], id[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], id[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], id[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], id[10:16])
	return string(encoded[:]), nil
}

func requestIDMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := newRequestID()
		if err != nil {
			logger.Error("failed to generate request ID", "error", err)
			writeJSON(logger, w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"code": "INTERNAL_ERROR", "message": "Internal server error"},
			})
			return
		}
		w.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestLoggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.Info("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
			"request_id", RequestID(r.Context()),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func corsMiddleware(allowedOrigins map[string]struct{}, next http.Handler) http.Handler {
	const allowedMethods = "GET, POST, PATCH, DELETE, OPTIONS"
	const allowedHeaders = "Content-Type, Idempotency-Key, X-CSRF-Token, X-Request-ID"
	headers := map[string]struct{}{
		"content-type":    {},
		"idempotency-key": {},
		"x-csrf-token":    {},
		"x-request-id":    {},
	}
	methods := map[string]struct{}{
		http.MethodGet:     {},
		http.MethodPost:    {},
		http.MethodPatch:   {},
		http.MethodDelete:  {},
		http.MethodOptions: {},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := allowedOrigins[origin]; !ok {
			if r.Method == http.MethodOptions {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
		if r.Method != http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}

		requestedMethod := r.Header.Get("Access-Control-Request-Method")
		if _, ok := methods[requestedMethod]; !ok || requestedMethod == http.MethodOptions {
			http.Error(w, "method not allowed", http.StatusForbidden)
			return
		}
		for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
			if header = strings.ToLower(strings.TrimSpace(header)); header != "" {
				if _, ok := headers[header]; !ok {
					http.Error(w, fmt.Sprintf("header %s not allowed", header), http.StatusForbidden)
					return
				}
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", allowedMethods)
		w.Header().Set("Access-Control-Allow-Headers", allowedHeaders)
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
	})
}
