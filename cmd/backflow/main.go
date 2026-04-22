package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/api"
	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/debug"
	"github.com/backflow-labs/backflow/internal/embeddings"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/orchestrator"
	orchdocker "github.com/backflow-labs/backflow/internal/orchestrator/docker"
	orchec2 "github.com/backflow-labs/backflow/internal/orchestrator/ec2"
	orchfargate "github.com/backflow-labs/backflow/internal/orchestrator/fargate"
	"github.com/backflow-labs/backflow/internal/orchestrator/outputs"
	orchs3 "github.com/backflow-labs/backflow/internal/orchestrator/s3"
	"github.com/backflow-labs/backflow/internal/store"
)

const (
	eventBusShutdownTimeout = 10 * time.Second
	keepaliveInterval       = 30 * time.Second
)

// setupLogger configures a zerolog.Logger. When logFile is empty, logs go to
// stderr only. When set, logs go to both stderr and the specified file path.
// Returns the logger, an io.Closer for the file (nil when stderr-only), and any error.
func setupLogger(logFile string) (zerolog.Logger, io.Closer, error) {
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}

	if logFile == "" {
		logger := zerolog.New(consoleWriter).With().Timestamp().Caller().Logger()
		return logger, nil, nil
	}

	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return zerolog.Logger{}, nil, fmt.Errorf("create log directory: %w", err)
	}

	f, err := os.Create(logFile)
	if err != nil {
		return zerolog.Logger{}, nil, fmt.Errorf("create log file: %w", err)
	}

	multi := io.MultiWriter(consoleWriter, f)
	logger := zerolog.New(multi).With().Timestamp().Caller().Logger()
	return logger, f, nil
}

// buildHTTPHandler wires the HTTP routes exposed by the server binary.
// Keeping this separate from main lets tests exercise the same routing
// composition that production uses.
func buildHTTPHandler(cfg *config.Config, db store.Store, poolStatter debug.PoolStatter, logs api.LogFetcher, bus notify.Emitter, runningFn func() int, startedAt time.Time) http.Handler {
	if runningFn == nil {
		runningFn = func() int { return 0 }
	}

	router := api.NewServer(db, cfg, logs, bus)

	// Debug stats endpoint (outside /api/v1/; auth is applied explicitly here).
	router.With(api.AuthMiddleware(db, cfg.APIKey)).Get("/debug/stats", debug.StatsHandler(runningFn, poolStatter, startedAt).ServeHTTP)

	return router
}

func main() {
	startedAt := time.Now()

	// Set up initial stderr-only logger; reconfigured after config load if LogFile is set.
	logger, _, err := setupLogger("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to set up logger: %v\n", err)
		os.Exit(1)
	}
	log.Logger = logger

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	if cfg.LogFile != "" {
		logger, closer, err := setupLogger(cfg.LogFile)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to set up log file")
		}
		defer closer.Close()
		log.Logger = logger
		log.Info().Str("log_file", cfg.LogFile).Msg("logging to file")
	}

	if cfg.RestrictAPI {
		log.Info().Msg("API access restricted: all /api/v1/* endpoints return 403")
	}

	db, err := store.NewPostgres(context.Background(), cfg.DatabaseURL, "migrations")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open database")
	}
	defer db.Close()

	// Create event bus and subscribe notification channels
	bus := notify.NewEventBus()

	if cfg.WebhookURL != "" {
		bus.Subscribe(notify.NewWebhookNotifier(cfg.WebhookURL, cfg.WebhookEvents))
		log.Info().Str("url", cfg.WebhookURL).Msg("webhook notifications enabled")
	}

	// Declare as the interface type so a nil *Uploader stays a nil interface
	// (avoids the nil-pointer-in-non-nil-interface trap in saveTaskMetadata).
	var s3Uploader orchestrator.S3Client
	if u, err := orchs3.NewUploader(context.Background(), cfg); err != nil {
		log.Fatal().Err(err).Msg("failed to create S3 uploader")
	} else if u != nil {
		s3Uploader = u
		log.Info().Str("bucket", cfg.S3Bucket).Msg("S3 storage enabled")
	}

	// Wire runner, scaler, and spot checker based on operating mode
	var runner orchestrator.Runner
	var scaler orchestrator.Scaler
	var spot orchestrator.SpotChecker

	switch cfg.Mode {
	case config.ModeLocal:
		runner = orchdocker.NewManager(cfg)
		scaler = orchestrator.NoopScaler{}
	case config.ModeFargate:
		runner = orchfargate.NewManager(cfg, s3Uploader)
		scaler = orchestrator.NoopScaler{}
	default:
		runner = orchdocker.NewManager(cfg)
		ec2mgr := orchec2.NewManager(cfg)
		scaler = orchec2.NewScaler(db, ec2mgr, cfg)
		spot = orchec2.NewSpotHandler(db, ec2mgr, bus)
	}

	var embedder embeddings.Embedder
	if cfg.OpenAIAPIKey != "" {
		embedder = embeddings.NewOpenAIEmbedder(cfg.OpenAIAPIKey, "", nil)
	}

	fsOutputs := outputs.New(cfg.DataDir)
	log.Info().Str("data_dir", cfg.DataDir).Msg("filesystem output writer enabled")

	orch := orchestrator.New(db, cfg, bus, runner, scaler, spot, s3Uploader, fsOutputs, embedder)
	handler := buildHTTPHandler(cfg, db, db, orch.Docker(), bus, orch.Running, startedAt)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start orchestrator in background
	go orch.Start(ctx)

	// Start self-ping keepalive to prevent Fly.io from stopping the machine
	// while tasks are pending or running.
	go keepalive(ctx, db, cfg.ListenAddr)

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
	if err := bus.CloseWithTimeout(eventBusShutdownTimeout); err != nil {
		log.Warn().Err(err).Dur("timeout", eventBusShutdownTimeout).Msg("event bus did not drain before shutdown timeout")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}

	log.Info().Msg("shutdown complete")
}

// keepalive periodically pings the local health endpoint while there are
// pending or running tasks, preventing Fly.io from stopping an idle machine
// in the middle of active work.
func keepalive(ctx context.Context, db store.Store, listenAddr string) {
	// Derive the health URL from the listen address (e.g. ":8080" → "http://localhost:8080/health").
	host := listenAddr
	if len(host) > 0 && host[0] == ':' {
		host = "localhost" + host
	}
	healthURL := "http://" + host + "/health"

	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if hasActiveTasks(ctx, db) {
				resp, err := client.Get(healthURL)
				if err != nil {
					log.Warn().Err(err).Msg("keepalive ping failed")
					continue
				}
				resp.Body.Close()
			}
		}
	}
}

// hasActiveTasks reports whether any tasks are in a non-terminal state.
func hasActiveTasks(ctx context.Context, db store.Store) bool {
	for _, status := range []models.TaskStatus{
		models.TaskStatusPending,
		models.TaskStatusProvisioning,
		models.TaskStatusRunning,
		models.TaskStatusRecovering,
		models.TaskStatusInterrupted,
	} {
		s := status
		tasks, err := db.ListTasks(ctx, store.TaskFilter{Status: &s, Limit: 1})
		if err != nil {
			continue
		}
		if len(tasks) > 0 {
			return true
		}
	}
	return false
}
