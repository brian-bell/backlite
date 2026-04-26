package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_PATH", "/tmp/backlite-test.db")
}

func TestLoad_UsesDefaultDatabasePath(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DatabasePath != "./backlite.db" {
		t.Fatalf("DatabasePath = %q, want ./backlite.db", cfg.DatabasePath)
	}
}

func TestLoad_DefaultModel(t *testing.T) {
	setBaseEnv(t)

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
}

func TestLoad_APIKey(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_API_KEY", "api-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.APIKey != "api-secret" {
		t.Fatalf("APIKey = %q, want %q", cfg.APIKey, "api-secret")
	}
}

func TestLoad_DataDir_Default(t *testing.T) {
	setBaseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("DataDir = %q, want %q (default)", cfg.DataDir, "./data")
	}
}

func TestLoad_DataDir_Set(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_DATA_DIR", "/var/lib/backlite")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DataDir != "/var/lib/backlite" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/var/lib/backlite")
	}
}

func TestLoad_LogFile_DefaultEmpty(t *testing.T) {
	setBaseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.LogFile != "" {
		t.Errorf("LogFile = %q, want empty string", cfg.LogFile)
	}
}

func TestLoad_LogFile_Set(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_LOG_FILE", "/tmp/backlite.log")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.LogFile != "/tmp/backlite.log" {
		t.Errorf("LogFile = %q, want %q", cfg.LogFile, "/tmp/backlite.log")
	}
}

func TestLoad_LocalBackupDefaults(t *testing.T) {
	setBaseEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if !cfg.LocalBackupEnabled {
		t.Fatal("LocalBackupEnabled = false, want true by default")
	}
	if cfg.LocalBackupDir != filepath.Join(home, "backlite-backups") {
		t.Fatalf("LocalBackupDir = %q, want %q", cfg.LocalBackupDir, filepath.Join(home, "backlite-backups"))
	}
	if cfg.LocalBackupInterval != 24*time.Hour {
		t.Fatalf("LocalBackupInterval = %v, want %v", cfg.LocalBackupInterval, 24*time.Hour)
	}
}

func TestLoad_LocalBackupOverrides(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_LOCAL_BACKUP_DIR", "~/custom-backups")
	t.Setenv("BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC", "7200")

	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.LocalBackupDir != filepath.Join(home, "custom-backups") {
		t.Fatalf("LocalBackupDir = %q, want %q", cfg.LocalBackupDir, filepath.Join(home, "custom-backups"))
	}
	if cfg.LocalBackupInterval != 2*time.Hour {
		t.Fatalf("LocalBackupInterval = %v, want %v", cfg.LocalBackupInterval, 2*time.Hour)
	}
}

func TestLoad_LocalBackupCanBeDisabled(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_LOCAL_BACKUP_ENABLED", "false")
	t.Setenv("BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.LocalBackupEnabled {
		t.Fatal("LocalBackupEnabled = true, want false")
	}
}

func TestLoad_LocalBackupRequiresPositiveIntervalWhenEnabled(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for zero local backup interval")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC") {
		t.Fatalf("error = %v, want interval env name", err)
	}
}

func TestLoad_LocalBackupRetentionDefaults(t *testing.T) {
	setBaseEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.LocalBackupRetention != 7*24*time.Hour {
		t.Fatalf("LocalBackupRetention = %v, want %v", cfg.LocalBackupRetention, 7*24*time.Hour)
	}
}

func TestLoad_LocalBackupRetentionRejectsNegative(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_LOCAL_BACKUP_RETENTION_SEC", "-1")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for negative local backup retention")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_LOCAL_BACKUP_RETENTION_SEC") {
		t.Fatalf("error = %v, want retention env name", err)
	}
}

func TestLoad_ReaderConfig(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_READER_IMAGE", "backlite-reader:v1")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_BUDGET", "0.5")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC", "300")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_TURNS", "20")
	t.Setenv("BACKFLOW_INTERNAL_API_BASE_URL", "http://host.docker.internal:8080")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ReaderImage != "backlite-reader:v1" {
		t.Errorf("ReaderImage = %q, want %q", cfg.ReaderImage, "backlite-reader:v1")
	}
	if cfg.DefaultReadMaxBudget != 0.5 {
		t.Errorf("DefaultReadMaxBudget = %v, want %v", cfg.DefaultReadMaxBudget, 0.5)
	}
	if cfg.DefaultReadMaxRuntime != 300*time.Second {
		t.Errorf("DefaultReadMaxRuntime = %v, want %v", cfg.DefaultReadMaxRuntime, 300*time.Second)
	}
	if cfg.DefaultReadMaxTurns != 20 {
		t.Errorf("DefaultReadMaxTurns = %d, want %d", cfg.DefaultReadMaxTurns, 20)
	}
	if cfg.InternalAPIBaseURL != "http://host.docker.internal:8080" {
		t.Errorf("InternalAPIBaseURL = %q", cfg.InternalAPIBaseURL)
	}
}

func TestLoad_ReaderConfig_UnsetDefaults(t *testing.T) {
	setBaseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ReaderImage != "" {
		t.Errorf("ReaderImage = %q, want empty when unset", cfg.ReaderImage)
	}
	if cfg.DefaultReadMaxBudget != 0 {
		t.Errorf("DefaultReadMaxBudget = %v, want 0 when unset", cfg.DefaultReadMaxBudget)
	}
	if cfg.DefaultReadMaxRuntime != 0 {
		t.Errorf("DefaultReadMaxRuntime = %v, want 0 when unset", cfg.DefaultReadMaxRuntime)
	}
	if cfg.DefaultReadMaxTurns != 0 {
		t.Errorf("DefaultReadMaxTurns = %d, want 0 when unset", cfg.DefaultReadMaxTurns)
	}
	if cfg.InternalAPIBaseURL != "" {
		t.Errorf("InternalAPIBaseURL = %q, want empty when unset", cfg.InternalAPIBaseURL)
	}
}

func setReaderEnv(t *testing.T) {
	t.Helper()
	setBaseEnv(t)
	t.Setenv("BACKFLOW_READER_IMAGE", "backlite-reader:v1")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_BUDGET", "0.5")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC", "300")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_TURNS", "20")
}

func TestLoad_ReaderImage_RequiresReadMaxBudget(t *testing.T) {
	setReaderEnv(t)
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_BUDGET", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DEFAULT_READ_MAX_BUDGET is unset with reader image")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_DEFAULT_READ_MAX_BUDGET") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_ReaderImage_RequiresReadMaxRuntime(t *testing.T) {
	setReaderEnv(t)
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC is unset with reader image")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_ReaderImage_RequiresReadMaxTurns(t *testing.T) {
	setReaderEnv(t)
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_TURNS", "0")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_DEFAULT_READ_MAX_TURNS is unset with reader image")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_DEFAULT_READ_MAX_TURNS") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func setNotifyEnv(t *testing.T) {
	t.Helper()
	setBaseEnv(t)
	t.Setenv("BACKFLOW_RESEND_API_KEY", "re_test")
	t.Setenv("BACKFLOW_NOTIFY_EMAIL_FROM", "from@example.com")
	t.Setenv("BACKFLOW_NOTIFY_EMAIL_TO", "to@example.com")
}

func TestLoad_NotifyConfig(t *testing.T) {
	setNotifyEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ResendAPIKey != "re_test" {
		t.Errorf("ResendAPIKey = %q, want %q", cfg.ResendAPIKey, "re_test")
	}
	if cfg.NotifyEmailFrom != "from@example.com" {
		t.Errorf("NotifyEmailFrom = %q, want %q", cfg.NotifyEmailFrom, "from@example.com")
	}
	if cfg.NotifyEmailTo != "to@example.com" {
		t.Errorf("NotifyEmailTo = %q, want %q", cfg.NotifyEmailTo, "to@example.com")
	}
}

func TestLoad_NotifyConfig_AllUnset(t *testing.T) {
	setBaseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ResendAPIKey != "" {
		t.Errorf("ResendAPIKey = %q, want empty when unset", cfg.ResendAPIKey)
	}
	if cfg.NotifyEmailFrom != "" {
		t.Errorf("NotifyEmailFrom = %q, want empty when unset", cfg.NotifyEmailFrom)
	}
	if cfg.NotifyEmailTo != "" {
		t.Errorf("NotifyEmailTo = %q, want empty when unset", cfg.NotifyEmailTo)
	}
}

func TestLoad_NotifyConfig_RequiresResendAPIKey(t *testing.T) {
	setNotifyEnv(t)
	t.Setenv("BACKFLOW_RESEND_API_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_RESEND_API_KEY is unset with email vars")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_RESEND_API_KEY") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_NotifyConfig_RequiresEmailFrom(t *testing.T) {
	setNotifyEnv(t)
	t.Setenv("BACKFLOW_NOTIFY_EMAIL_FROM", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_NOTIFY_EMAIL_FROM is unset with other notify vars")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_NOTIFY_EMAIL_FROM") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_NotifyConfig_RequiresEmailTo(t *testing.T) {
	setNotifyEnv(t)
	t.Setenv("BACKFLOW_NOTIFY_EMAIL_TO", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_NOTIFY_EMAIL_TO is unset with other notify vars")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_NOTIFY_EMAIL_TO") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_NotifyConfig_FromMissingAt(t *testing.T) {
	setNotifyEnv(t)
	t.Setenv("BACKFLOW_NOTIFY_EMAIL_FROM", "notanemail")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_NOTIFY_EMAIL_FROM lacks '@'")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_NOTIFY_EMAIL_FROM") || !strings.Contains(err.Error(), "@") {
		t.Errorf("error should name the var and mention '@', got: %v", err)
	}
}

func TestLoad_NotifyConfig_ToMissingAt(t *testing.T) {
	setNotifyEnv(t)
	t.Setenv("BACKFLOW_NOTIFY_EMAIL_TO", "notanemail")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_NOTIFY_EMAIL_TO lacks '@'")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_NOTIFY_EMAIL_TO") || !strings.Contains(err.Error(), "@") {
		t.Errorf("error should name the var and mention '@', got: %v", err)
	}
}
