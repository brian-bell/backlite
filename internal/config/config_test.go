package config

import (
	"strings"
	"testing"
)

func TestLoad_MissingDatabaseURL(t *testing.T) {
	// Set minimum env vars to pass earlier validations
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DATABASE_URL is empty, got nil")
	}

	want := "BACKFLOW_DATABASE_URL"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error should mention %q, got: %s", want, err.Error())
	}
}

func TestLoad_DefaultModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.DefaultClaudeModel == "" {
		t.Error("DefaultClaudeModel is empty")
	}
	if cfg.DefaultCodexModel == "" {
		t.Error("DefaultCodexModel is empty")
	}
	if cfg.SlackEvents != nil {
		t.Errorf("SlackEvents = %#v, want nil when unset", cfg.SlackEvents)
	}
	if cfg.DiscordEvents != nil {
		t.Errorf("DiscordEvents = %#v, want nil when unset", cfg.DiscordEvents)
	}
}

func TestLoad_SlackAndDiscordEvents(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/test")
	t.Setenv("BACKFLOW_SLACK_EVENTS", "task.created, task.completed ,task.failed")
	t.Setenv("BACKFLOW_DISCORD_EVENTS", "task.running, task.interrupted")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	wantSlack := []string{"task.created", "task.completed", "task.failed"}
	if len(cfg.SlackEvents) != len(wantSlack) {
		t.Fatalf("SlackEvents length = %d, want %d", len(cfg.SlackEvents), len(wantSlack))
	}
	for i := range wantSlack {
		if cfg.SlackEvents[i] != wantSlack[i] {
			t.Fatalf("SlackEvents[%d] = %q, want %q", i, cfg.SlackEvents[i], wantSlack[i])
		}
	}

	wantDiscord := []string{"task.running", "task.interrupted"}
	if len(cfg.DiscordEvents) != len(wantDiscord) {
		t.Fatalf("DiscordEvents length = %d, want %d", len(cfg.DiscordEvents), len(wantDiscord))
	}
	for i := range wantDiscord {
		if cfg.DiscordEvents[i] != wantDiscord[i] {
			t.Fatalf("DiscordEvents[%d] = %q, want %q", i, cfg.DiscordEvents[i], wantDiscord[i])
		}
	}
}

func TestLoad_DiscordEnabled(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_DISCORD_APP_ID", "123456789")
	t.Setenv("BACKFLOW_DISCORD_PUBLIC_KEY", "abc123hex")
	t.Setenv("BACKFLOW_DISCORD_BOT_TOKEN", "Bot secret-token")
	t.Setenv("BACKFLOW_DISCORD_GUILD_ID", "guild-1")
	t.Setenv("BACKFLOW_DISCORD_CHANNEL_ID", "channel-1")
	t.Setenv("BACKFLOW_DISCORD_ALLOWED_ROLES", "role-a, role-b")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if !cfg.DiscordEnabled() {
		t.Fatal("DiscordEnabled() = false, want true")
	}
	if cfg.DiscordAppID != "123456789" {
		t.Errorf("DiscordAppID = %q, want %q", cfg.DiscordAppID, "123456789")
	}
	if cfg.DiscordPublicKey != "abc123hex" {
		t.Errorf("DiscordPublicKey = %q, want %q", cfg.DiscordPublicKey, "abc123hex")
	}
	if cfg.DiscordBotToken != "Bot secret-token" {
		t.Errorf("DiscordBotToken = %q, want %q", cfg.DiscordBotToken, "Bot secret-token")
	}
	if cfg.DiscordGuildID != "guild-1" {
		t.Errorf("DiscordGuildID = %q, want %q", cfg.DiscordGuildID, "guild-1")
	}
	if cfg.DiscordChannelID != "channel-1" {
		t.Errorf("DiscordChannelID = %q, want %q", cfg.DiscordChannelID, "channel-1")
	}
	wantRoles := []string{"role-a", "role-b"}
	if len(cfg.DiscordAllowedRoles) != len(wantRoles) {
		t.Fatalf("DiscordAllowedRoles length = %d, want %d", len(cfg.DiscordAllowedRoles), len(wantRoles))
	}
	for i := range wantRoles {
		if cfg.DiscordAllowedRoles[i] != wantRoles[i] {
			t.Errorf("DiscordAllowedRoles[%d] = %q, want %q", i, cfg.DiscordAllowedRoles[i], wantRoles[i])
		}
	}
}

func TestLoad_DiscordNotConfigured(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DiscordEnabled() {
		t.Fatal("DiscordEnabled() = true, want false when no Discord vars set")
	}
}

func TestLoad_DiscordAppID_MissingPublicKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_DISCORD_APP_ID", "123456789")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DISCORD_PUBLIC_KEY is missing")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_DISCORD_PUBLIC_KEY") {
		t.Errorf("error should mention BACKFLOW_DISCORD_PUBLIC_KEY, got: %s", err)
	}
}

func TestLoad_DiscordAppID_MissingBotToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_DISCORD_APP_ID", "123456789")
	t.Setenv("BACKFLOW_DISCORD_PUBLIC_KEY", "abc123hex")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DISCORD_BOT_TOKEN is missing")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_DISCORD_BOT_TOKEN") {
		t.Errorf("error should mention BACKFLOW_DISCORD_BOT_TOKEN, got: %s", err)
	}
}

func TestLoad_DiscordAppID_MissingGuildID(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_DISCORD_APP_ID", "123456789")
	t.Setenv("BACKFLOW_DISCORD_PUBLIC_KEY", "abc123hex")
	t.Setenv("BACKFLOW_DISCORD_BOT_TOKEN", "Bot secret-token")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DISCORD_GUILD_ID is missing")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_DISCORD_GUILD_ID") {
		t.Errorf("error should mention BACKFLOW_DISCORD_GUILD_ID, got: %s", err)
	}
}

func TestLoad_DiscordAppID_MissingChannelID(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_DISCORD_APP_ID", "123456789")
	t.Setenv("BACKFLOW_DISCORD_PUBLIC_KEY", "abc123hex")
	t.Setenv("BACKFLOW_DISCORD_BOT_TOKEN", "Bot secret-token")
	t.Setenv("BACKFLOW_DISCORD_GUILD_ID", "guild-1")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DISCORD_CHANNEL_ID is missing")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_DISCORD_CHANNEL_ID") {
		t.Errorf("error should mention BACKFLOW_DISCORD_CHANNEL_ID, got: %s", err)
	}
}
