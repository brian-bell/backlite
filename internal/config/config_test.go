package config

import (
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
