package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/idempotent-payment-api/internal/config"
	appHTTP "github.com/example/idempotent-payment-api/internal/http"
	"github.com/example/idempotent-payment-api/internal/idempotency"
	"github.com/example/idempotent-payment-api/internal/payment"
	"github.com/example/idempotent-payment-api/internal/processor"
	"github.com/example/idempotent-payment-api/internal/store"
)

func main() {
	// 1. Load application configuration from environment
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: configuration failure: %v\n", err)
		os.Exit(1)
	}

	// 2. Setup structured logging
	var logLevel slog.Level
	switch cfg.Log.Level {
	case "DEBUG":
		logLevel = slog.LevelDebug
	case "WARN":
		logLevel = slog.LevelWarn
	case "ERROR":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	logger.Info("starting idempotent payment API service",
		slog.String("port", cfg.Server.Port),
		slog.String("env", cfg.Server.Env),
		slog.String("processor_mode", cfg.Processor.DefaultMode),
	)

	// Root context cancelled on SIGINT / SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 3. Connect to PostgreSQL
	dbPool, err := store.NewPool(ctx, cfg.Database)
	if err != nil {
		logger.Error("failed to connect to postgresql database", slog.Any("error", err))
		os.Exit(1)
	}
	defer dbPool.Close()

	// 4. Run database migrations
	logger.Info("applying database migrations")
	if err := store.MigrateUp(cfg.Database.DSN()); err != nil {
		logger.Error("database migration failed", slog.Any("error", err))
		os.Exit(1)
	}
	logger.Info("database migrations applied successfully")

	// 5. Initialize repositories and domain services
	paymentRepo := store.NewPaymentRepo(dbPool)
	idempotencyRepo := store.NewIdempotencyRepo(dbPool)

	paymentSvc := payment.NewService(paymentRepo)
	idempotencySvc := idempotency.NewService(idempotencyRepo, cfg.Idempotency.KeyTTL, cfg.Idempotency.RecoveryTimeout)

	// 6. Initialize payment processor
	proc := processor.NewSimulatedProcessor(processor.Mode(cfg.Processor.DefaultMode))

	// 7. Start background expired key cleanup worker
	go startCleanupWorker(ctx, idempotencySvc, logger, 30*time.Minute)

	// 8. Assemble HTTP handlers and router
	handler := appHTTP.NewHandler(paymentSvc, idempotencySvc, proc, dbPool, logger)
	router := appHTTP.NewRouter(handler, logger)

	srv := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// 9. Start HTTP server in background
	serverErrChan := make(chan error, 1)
	go func() {
		logger.Info("server listening", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrChan <- err
		}
	}()

	// 10. Wait for shutdown signal or server error
	select {
	case err := <-serverErrChan:
		logger.Error("server listener error", slog.Any("error", err))
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("shutdown signal received; commencing graceful teardown")
	}

	// 11. Graceful shutdown
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.Server.ShutdownWait)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed; forcing termination", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("server exited cleanly")
}

func startCleanupWorker(ctx context.Context, svc *idempotency.Service, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := svc.Cleanup(ctx)
			if err != nil {
				logger.WarnContext(ctx, "expired idempotency key cleanup encountered error", slog.Any("error", err))
			} else if deleted > 0 {
				logger.InfoContext(ctx, "cleaned up expired idempotency keys", slog.Int64("deleted_count", deleted))
			}
		}
	}
}
