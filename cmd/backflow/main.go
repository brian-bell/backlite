package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/api"
	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/orchestrator"
	"github.com/backflow-labs/backflow/internal/store"
)

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}).
		With().Timestamp().Caller().Logger()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	db, err := store.NewSQLite(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open database")
	}
	defer db.Close()

	var notifier notify.Notifier
	if cfg.WebhookURL != "" {
		notifier = notify.NewWebhookNotifier(cfg.WebhookURL, cfg.WebhookEvents)
		log.Info().Str("url", cfg.WebhookURL).Msg("webhook notifications enabled")
	} else {
		notifier = notify.NoopNotifier{}
	}

	orch := orchestrator.New(db, cfg, notifier)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      api.NewServer(db, cfg),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start orchestrator in background
	go orch.Start(ctx)

	// Start HTTP server
	go func() {
		log.Info().Str("addr", cfg.ListenAddr).Msg("API server starting")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server failed")
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down...")
	cancel()
	orch.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}

	log.Info().Msg("shutdown complete")
}
