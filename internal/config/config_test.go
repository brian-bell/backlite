package config

import (
	"strings"
	"testing"
	"time"
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
}

func TestLoad_RestrictAPI(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_RESTRICT_API", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !cfg.RestrictAPI {
		t.Error("RestrictAPI = false, want true when BACKFLOW_RESTRICT_API=true")
	}
}

func TestLoad_RestrictAPI_DefaultFalse(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.RestrictAPI {
		t.Error("RestrictAPI = true, want false when BACKFLOW_RESTRICT_API is unset")
	}
}

func TestLoad_APIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
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
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DataDir != "./data" {
		t.Errorf("DataDir = %q, want %q (default)", cfg.DataDir, "./data")
	}
}

func TestLoad_DataDir_Set(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_DATA_DIR", "/var/lib/backflow")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.DataDir != "/var/lib/backflow" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/var/lib/backflow")
	}
}

func TestLoad_LogFile_DefaultEmpty(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.LogFile != "" {
		t.Errorf("LogFile = %q, want empty string", cfg.LogFile)
	}
}

func TestLoad_LogFile_Set(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_LOG_FILE", "/tmp/backflow.log")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.LogFile != "/tmp/backflow.log" {
		t.Errorf("LogFile = %q, want %q", cfg.LogFile, "/tmp/backflow.log")
	}
}

func TestLoad_ReaderConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_READER_IMAGE", "backflow-reader:v1")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_BUDGET", "0.5")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC", "300")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_TURNS", "20")
	t.Setenv("SUPABASE_URL", "https://test.supabase.co")
	t.Setenv("SUPABASE_ANON_KEY", "sb_publishable_test")
	t.Setenv("BACKFLOW_ECS_READER_TASK_DEFINITION", "backflow-reader-td:3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.ReaderImage != "backflow-reader:v1" {
		t.Errorf("ReaderImage = %q, want %q", cfg.ReaderImage, "backflow-reader:v1")
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
	if cfg.SupabaseURL != "https://test.supabase.co" {
		t.Errorf("SupabaseURL = %q, want %q", cfg.SupabaseURL, "https://test.supabase.co")
	}
	if cfg.SupabaseAnonKey != "sb_publishable_test" {
		t.Errorf("SupabaseAnonKey = %q, want %q", cfg.SupabaseAnonKey, "sb_publishable_test")
	}
	if cfg.ECSReaderTaskDefinition != "backflow-reader-td:3" {
		t.Errorf("ECSReaderTaskDefinition = %q, want %q", cfg.ECSReaderTaskDefinition, "backflow-reader-td:3")
	}
}

func TestLoad_ReaderConfig_UnsetDefaults(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")

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
	if cfg.SupabaseURL != "" {
		t.Errorf("SupabaseURL = %q, want empty when unset", cfg.SupabaseURL)
	}
	if cfg.SupabaseAnonKey != "" {
		t.Errorf("SupabaseAnonKey = %q, want empty when unset", cfg.SupabaseAnonKey)
	}
	if cfg.ECSReaderTaskDefinition != "" {
		t.Errorf("ECSReaderTaskDefinition = %q, want empty when unset", cfg.ECSReaderTaskDefinition)
	}
}

// setReaderEnv populates every required read-mode env var. Individual
// "missing X" tests unset one var after calling this helper.
func setReaderEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("BACKFLOW_READER_IMAGE", "backflow-reader:v1")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_BUDGET", "0.5")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC", "300")
	t.Setenv("BACKFLOW_DEFAULT_READ_MAX_TURNS", "20")
	t.Setenv("SUPABASE_URL", "https://test.supabase.co")
	t.Setenv("SUPABASE_ANON_KEY", "sb_publishable_test")
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

func TestLoad_ReaderImage_RequiresSupabaseURL(t *testing.T) {
	setReaderEnv(t)
	t.Setenv("SUPABASE_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when SUPABASE_URL is unset with reader image")
	}
	if !strings.Contains(err.Error(), "SUPABASE_URL") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_ReaderImage_RequiresSupabaseAnonKey(t *testing.T) {
	setReaderEnv(t)
	t.Setenv("SUPABASE_ANON_KEY", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when SUPABASE_ANON_KEY is unset with reader image")
	}
	if !strings.Contains(err.Error(), "SUPABASE_ANON_KEY") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_ReaderImage_Fargate_RequiresReaderTaskDefinition(t *testing.T) {
	setReaderEnv(t)
	// Fargate mode with the standard required vars, reader image set, but no reader task def.
	t.Setenv("BACKFLOW_MODE", "fargate")
	t.Setenv("BACKFLOW_ECS_CLUSTER", "cluster")
	t.Setenv("BACKFLOW_ECS_TASK_DEFINITION", "code-td")
	t.Setenv("BACKFLOW_ECS_SUBNETS", "subnet-1")
	t.Setenv("BACKFLOW_CLOUDWATCH_LOG_GROUP", "/backflow")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when BACKFLOW_ECS_READER_TASK_DEFINITION is unset in fargate mode with reader image")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_ECS_READER_TASK_DEFINITION") {
		t.Errorf("error should name the missing env var, got: %v", err)
	}
}

func TestLoad_ReaderImage_NonFargate_DoesNotRequireReaderTaskDefinition(t *testing.T) {
	setReaderEnv(t)
	t.Setenv("BACKFLOW_MODE", "local")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
}
