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

	"filemgr/internal/audit"
	"filemgr/internal/auth"
	"filemgr/internal/config"
	"filemgr/internal/db"
	"filemgr/internal/downloads"
	"filemgr/internal/files"
	"filemgr/internal/httpapi"
	"filemgr/internal/observability"
	"filemgr/internal/shares"
	"filemgr/internal/storage"
	"filemgr/internal/uploads"
	"filemgr/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		url := "http://127.0.0.1:8080/health/live"
		for i := 2; i < len(os.Args)-1; i++ {
			if os.Args[i] == "--url" {
				url = os.Args[i+1]
			}
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(url)
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger := observability.InitLogger(cfg.AppEnv)
	slog.SetDefault(logger)
	slog.Info("starting file manager",
		"env", cfg.AppEnv,
		"admin_host", cfg.AppAdminHost,
		"share_host", cfg.AppShareHost,
		"data_dir", cfg.DataDir,
	)

	// Open database & run migrations
	database, err := db.Open(cfg.DBPath, cfg.DBBusyTimeout, cfg.SQLiteSynchronous)
	if err != nil {
		slog.Error("failed to initialize sqlite database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Initialize storage layout
	store, err := storage.NewStorage(cfg.DataDir)
	if err != nil {
		slog.Error("failed to initialize storage engine", "error", err)
		os.Exit(1)
	}

	// Initialize domain services
	filesSvc := files.NewService(database)
	uploadsSvc := uploads.NewService(database, store, filesSvc, cfg)
	downloadsSvc := downloads.NewService(store, filesSvc, cfg)
	sharesSvc := shares.NewService(database, filesSvc, cfg)
	auditSvc := audit.NewService(database)
	authVerifier := auth.NewVerifier(cfg)

	// Run startup reconciler to clean up or finalize interrupted uploads
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := uploadsSvc.Reconcile(startupCtx); err != nil {
		slog.Warn("startup reconciler encountered errors", "error", err)
	} else {
		slog.Info("startup reconciler completed successfully")
	}
	startupCancel()

	// Background ticker for periodic reconciler
	reconcilerStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				if err := uploadsSvc.Reconcile(ctx); err != nil {
					slog.Warn("periodic reconciler warning", "error", err)
				}
				cancel()
			case <-reconcilerStop:
				return
			}
		}
	}()

	// Build HTTP router
	api := httpapi.NewAPI(
		cfg,
		database,
		filesSvc,
		uploadsSvc,
		downloadsSvc,
		sharesSvc,
		auditSvc,
		authVerifier,
		store,
	)

	server := &http.Server{
		Addr:         cfg.AppListenAddr,
		Handler:      api.Handler(web.DistFS()),
		ReadTimeout:  30 * time.Minute, // Large for tus uploads
		WriteTimeout: 60 * time.Minute, // Large for streaming multi-GB downloads
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info("server listening", "addr", cfg.AppListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server listen failed", "error", err)
			os.Exit(1)
		}
	}()

	<-stopChan
	slog.Info("shutdown signal received, initiating graceful shutdown")
	close(reconcilerStop)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
	} else {
		slog.Info("server shut down gracefully")
	}
}
