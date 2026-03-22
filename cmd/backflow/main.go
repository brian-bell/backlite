package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/api"
	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/discord"
	"github.com/backflow-labs/backflow/internal/messaging"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/orchestrator"
	orchdocker "github.com/backflow-labs/backflow/internal/orchestrator/docker"
	orchec2 "github.com/backflow-labs/backflow/internal/orchestrator/ec2"
	orchfargate "github.com/backflow-labs/backflow/internal/orchestrator/fargate"
	orchs3 "github.com/backflow-labs/backflow/internal/orchestrator/s3"
	"github.com/backflow-labs/backflow/internal/store"
)

const eventBusShutdownTimeout = 10 * time.Second

func main() {
	if err := os.MkdirAll("logs", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logs directory: %v\n", err)
		os.Exit(1)
	}
	logFileName := fmt.Sprintf("logs/server_%s.log", time.Now().Format("20060102_150405"))
	logFile, err := os.Create(logFileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create log file: %v\n", err)
		os.Exit(1)
	}
	defer logFile.Close()

	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	multi := io.MultiWriter(consoleWriter, logFile)
	log.Logger = zerolog.New(multi).With().Timestamp().Caller().Logger()
	log.Info().Str("log_file", logFileName).Msg("logging to file")

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
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
	logConfiguredNotificationChannels(cfg)

	// Initialize messaging
	var messenger messaging.Messenger
	switch cfg.SMSProvider {
	case "twilio":
		messenger = messaging.NewTwilioMessenger(cfg.TwilioAccountSID, cfg.TwilioAuthToken, cfg.SMSFromNumber)
		log.Info().Str("from", cfg.SMSFromNumber).Msg("twilio SMS messaging enabled")
	default:
		messenger = messaging.NoopMessenger{}
	}

	if cfg.SMSProvider != "" {
		bus.Subscribe(notify.NewMessagingNotifier(messenger, cfg.SMSEvents))
	}

	s3Uploader, err := orchs3.NewUploader(context.Background(), cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create S3 uploader")
	}
	if s3Uploader != nil {
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
		spot = orchec2.NewSpotHandler(db, ec2mgr)
	}

	orch := orchestrator.New(db, cfg, bus, runner, scaler, spot, s3Uploader)

	router := api.NewServer(db, cfg, orch.Docker())

	// Mount SMS inbound webhook if provider is configured
	if cfg.SMSProvider != "" {
		router.Post("/webhooks/sms/inbound", messaging.InboundHandler(db, cfg, messenger))
		log.Info().Msg("SMS inbound webhook mounted at /webhooks/sms/inbound")
	}

	// Discord integration
	if cfg.DiscordEnabled() {
		pubKey, err := discord.ParsePublicKey(cfg.DiscordPublicKey)
		if err != nil {
			log.Fatal().Err(err).Msg("invalid BACKFLOW_DISCORD_PUBLIC_KEY")
		}
		router.Post("/webhooks/discord", discord.InteractionHandler(pubKey))

		now := time.Now().UTC()
		install := &models.DiscordInstall{
			GuildID:      cfg.DiscordGuildID,
			AppID:        cfg.DiscordAppID,
			ChannelID:    cfg.DiscordChannelID,
			AllowedRoles: cfg.DiscordAllowedRoles,
			InstalledAt:  now,
			UpdatedAt:    now,
		}
		if err := db.UpsertDiscordInstall(context.Background(), install); err != nil {
			log.Fatal().Err(err).Msg("failed to persist discord install state")
		}

		if err := discord.RegisterCommands("", cfg.DiscordAppID, cfg.DiscordBotToken); err != nil {
			log.Error().Err(err).Msg("failed to register discord slash commands")
		}

		bus.Subscribe(notify.NewDiscordNotifier(cfg.DiscordEvents))
		log.Info().Str("guild_id", cfg.DiscordGuildID).Msg("discord integration enabled")
	}

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      router,
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

func logConfiguredNotificationChannels(cfg *config.Config) {
	if cfg.SlackWebhookURL != "" {
		log.Info().Msg("slack notifications configured but subscriber not yet implemented")
	}
}
