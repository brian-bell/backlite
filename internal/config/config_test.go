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

func TestLoad_BackupS3UploadDefaultsDisabled(t *testing.T) {
	setBaseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.BackupS3Bucket != "" {
		t.Fatalf("BackupS3Bucket = %q, want empty by default", cfg.BackupS3Bucket)
	}
	if cfg.BackupS3Prefix != "" {
		t.Fatalf("BackupS3Prefix = %q, want empty by default", cfg.BackupS3Prefix)
	}
	if cfg.BackupS3Region != "" {
		t.Fatalf("BackupS3Region = %q, want empty by default", cfg.BackupS3Region)
	}
	if cfg.BackupS3Endpoint != "" {
		t.Fatalf("BackupS3Endpoint = %q, want empty by default", cfg.BackupS3Endpoint)
	}
	if cfg.BackupS3PathStyle {
		t.Fatal("BackupS3PathStyle = true, want false by default")
	}
}

func TestLoad_BackupS3UploadConfig(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("BACKFLOW_BACKUP_S3_BUCKET", "backlite-prod-backups")
	t.Setenv("BACKFLOW_BACKUP_S3_PREFIX", "sqlite/daily/")
	t.Setenv("BACKFLOW_BACKUP_S3_REGION", "us-east-2")
	t.Setenv("BACKFLOW_BACKUP_S3_ENDPOINT", "https://s3.us-east-2.amazonaws.com")
	t.Setenv("BACKFLOW_BACKUP_S3_PATH_STYLE", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.BackupS3Bucket != "backlite-prod-backups" {
		t.Fatalf("BackupS3Bucket = %q, want backlite-prod-backups", cfg.BackupS3Bucket)
	}
	if cfg.BackupS3Prefix != "sqlite/daily/" {
		t.Fatalf("BackupS3Prefix = %q, want sqlite/daily/", cfg.BackupS3Prefix)
	}
	if cfg.BackupS3Region != "us-east-2" {
		t.Fatalf("BackupS3Region = %q, want us-east-2", cfg.BackupS3Region)
	}
	if cfg.BackupS3Endpoint != "https://s3.us-east-2.amazonaws.com" {
		t.Fatalf("BackupS3Endpoint = %q, want https://s3.us-east-2.amazonaws.com", cfg.BackupS3Endpoint)
	}
	if !cfg.BackupS3PathStyle {
		t.Fatal("BackupS3PathStyle = false, want true")
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
