package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	googleadapter "technical-service-backend/internal/adapters/google"
	httpadapter "technical-service-backend/internal/adapters/http"
	"technical-service-backend/internal/adapters/postgres"
	"technical-service-backend/internal/adapters/supabase"
	"technical-service-backend/internal/application"
	"technical-service-backend/internal/config"
	"technical-service-backend/internal/observability"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid backend configuration", "error", err)
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.LogLevel, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("backend stopped with an error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.NewPool(cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	connectCtx, cancelConnect := context.WithTimeout(ctx, cfg.DatabaseConnectTimeout)
	err = pool.Ping(connectCtx)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	authRepository := postgres.NewAuthRepository(pool, cfg.DatabaseQueryTimeout)
	identityProvider := supabase.NewIdentityProvider(cfg.Auth, nil)
	authService := application.NewAuthService(identityProvider, authRepository, cfg.Auth.SessionTTL)
	clientRepository := postgres.NewClientRepository(pool, cfg.DatabaseQueryTimeout)
	clientService := application.NewClientUseCases(clientRepository)
	technicianRepository := postgres.NewTechnicianRepository(pool, cfg.DatabaseQueryTimeout)
	technicianService := application.NewTechnicianUseCases(technicianRepository, identityProvider)
	serviceRepository := postgres.NewServiceRepository(pool, cfg.DatabaseQueryTimeout)
	serviceService := application.NewServiceUseCases(serviceRepository)
	paymentRepository := postgres.NewPaymentRepository(pool, cfg.DatabaseQueryTimeout)
	paymentService := application.NewPaymentUseCases(paymentRepository)
	estimateRepository := postgres.NewEstimateRepository(pool, cfg.DatabaseQueryTimeout)
	estimateService := application.NewEstimateUseCases(estimateRepository)
	servicePartRepository := postgres.NewServicePartRepository(pool, cfg.DatabaseQueryTimeout)
	servicePartService := application.NewServicePartUseCases(servicePartRepository)
	expenseRepository := postgres.NewExpenseRepository(pool, cfg.DatabaseQueryTimeout)
	expenseService := application.NewExpenseUseCases(expenseRepository)
	catalogRepository := postgres.NewCatalogRepository(pool, cfg.DatabaseQueryTimeout)
	catalogService := application.NewCatalogUseCases(catalogRepository)
	catalogAdminRepository := postgres.NewCatalogAdminRepository(pool, cfg.DatabaseQueryTimeout)
	catalogAdminService := application.NewCatalogAdminUseCases(catalogAdminRepository)
	catalogImportRepository := postgres.NewCatalogImportRepository(pool, cfg.DatabaseQueryTimeout)
	catalogImportReader := googleadapter.NewSheetsCatalogReader(
		cfg.GoogleSheets.CredentialsJSON, cfg.GoogleSheets.SpreadsheetID, cfg.GoogleSheets.CatalogRange,
	)
	catalogImportService := application.NewCatalogImportUseCases(catalogImportRepository, catalogImportReader)
	settlementRepository := postgres.NewSettlementRepository(pool, cfg.DatabaseQueryTimeout)
	settlementService := application.NewSettlementUseCases(settlementRepository)
	idempotencyRepository := postgres.NewIdempotencyRepository(pool, cfg.DatabaseQueryTimeout)
	handler := httpadapter.NewHandlerWithPhase6Complete(
		pool, cfg.CORSAllowedOrigins, authService, clientService, technicianService,
		serviceService, paymentService, estimateService, servicePartService,
		expenseService, catalogService, catalogAdminService, catalogImportService, settlementService,
		cfg.Auth, logger, idempotencyRepository,
	)
	server := httpadapter.NewServer(cfg.APIPort, handler)
	return httpadapter.Serve(ctx, server, logger)
}
