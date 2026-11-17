package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"kenosm/internal/browser"
	"kenosm/internal/config"
	"kenosm/internal/crypto"
	"kenosm/internal/db"
	"kenosm/internal/desktop"
	"kenosm/internal/handler"
	"kenosm/internal/logging"
	"kenosm/internal/repository"
	"kenosm/internal/server"
	"kenosm/internal/transport"
	"kenosm/internal/webui"
	"kenosm/internal/workspace"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	cipher, err := crypto.New(cfg.SecretKey)
	if err != nil {
		logger.Error("init cipher", "err", err)
		os.Exit(1)
	}

	serviceRepo := repository.NewServiceRepo(pool, cipher)
	logRepo := repository.NewLogRepo(pool)
	usageRepo := repository.NewUsageRepo(pool)
	chatRepo := repository.NewChatRepo(pool)
	activityRepo := repository.NewActivityRepo(pool)

	transportCache := transport.NewCache()
	reqLogger := logging.New(logRepo, logger, 1024)
	defer reqLogger.Close()

	// Periodically prune old request logs so the high-volume table stays bounded.
	if cfg.LogRetentionDays > 0 {
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					cutoff := time.Now().Add(-time.Duration(cfg.LogRetentionDays) * 24 * time.Hour)
					if n, err := logRepo.Prune(ctx, cutoff); err != nil {
						logger.Error("prune request logs", "err", err)
					} else if n > 0 {
						logger.Info("pruned request logs", "rows", n)
					}
				}
			}
		}()
	}

	ws, err := workspace.New(cfg.WorkspaceDir, cfg.CommandTimeout)
	if err != nil {
		logger.Error("init workspace", "err", err)
		os.Exit(1)
	}

	br, err := browser.New(browser.Config{})
	if err != nil {
		logger.Error("init browser", "err", err)
		os.Exit(1)
	}

	// Desktop automation (launch programs, screenshots, mouse/keyboard) is
	// optional: if xdotool or a screenshot tool is missing we disable it with a
	// warning instead of refusing to start.
	dsk, err := desktop.New(desktop.Config{})
	if err != nil {
		logger.Warn("desktop automation disabled", "err", err)
		dsk = nil
	}
	if dsk != nil {
		defer dsk.Close()
	}

	admin := handler.NewAdmin(serviceRepo)
	gateway := handler.NewGateway(serviceRepo, transportCache, reqLogger)
	swarmHandler := handler.NewSwarm(serviceRepo, usageRepo, chatRepo, cfg.OrchestratorModel, cfg.GeminiModel, cfg.AvailableModels, ws, br, dsk, cfg.Prices, activityRepo, transportCache, cfg.DisabledModules, cfg.MaxConcurrentRuns, cfg.PolishMaxRounds, cfg.AgentMaxIterations)
	wsHandler := handler.NewWorkspace(ws)
	statsHandler := handler.NewStats(usageRepo)
	activityHandler := handler.NewActivity(activityRepo)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(admin, gateway, swarmHandler, wsHandler, statsHandler, activityHandler, webui.Handler(), cfg.APIKey),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}
