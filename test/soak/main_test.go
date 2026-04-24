package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/backflow-labs/backflow/internal/store"
)

func TestTruncateTasks_ClearsCurrentSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "truncate-soak.db")

	s, err := store.NewSQLite(ctx, dbPath, filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close store: %v", err)
		}
	})

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("Close db: %v", err)
		}
	})

	if _, err := db.ExecContext(ctx, `
		INSERT INTO instances (instance_id, status, created_at, updated_at)
		VALUES ('local', 'running', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
		INSERT INTO tasks (id, status, task_mode, harness, prompt, created_at, updated_at)
		VALUES ('bf_01TESTSOAK0000000000000001', 'completed', 'read', 'claude_code', 'https://example.com', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
		INSERT INTO api_keys (key_hash, name, permissions, created_at, updated_at)
		VALUES ('hash-1', 'test', '[]', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
		INSERT INTO readings (id, task_id, url, title, tldr, created_at)
		VALUES ('bf_01TESTSOAK0000000000000002', 'bf_01TESTSOAK0000000000000001', 'https://example.com', 'Example', 'TLDR', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
	`); err != nil {
		t.Fatalf("seed db: %v", err)
	}

	truncateTasks(dbPath)

	for _, table := range []string{"readings", "api_keys", "tasks", "instances"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s row count = %d, want 0", table, count)
		}
	}
}

func TestDefaultSoakDatabasePath(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{name: "empty", base: "", want: "./backflow-soak.db"},
		{name: "sqlite file", base: "./backflow.db", want: "./backflow-soak.db"},
		{name: "already soak file", base: "./backflow-soak.db", want: "./backflow-soak.db"},
		{name: "non db path", base: "./tmp/backflow", want: "./tmp/backflow-soak.db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultSoakDatabasePath(tt.base); got != tt.want {
				t.Fatalf("defaultSoakDatabasePath(%q) = %q, want %q", tt.base, got, tt.want)
			}
		})
	}
}

func TestBuildSoakServerEnv(t *testing.T) {
	t.Setenv("BACKFLOW_CONTAINERS_PER_INSTANCE", "4")
	t.Setenv("BACKFLOW_API_KEY", "secret")
	t.Setenv("BACKFLOW_WEBHOOK_URL", "https://example.com/webhook")
	t.Setenv("BACKFLOW_WEBHOOK_EVENTS", "task.completed")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("ANTHROPIC_API_KEY", "")

	env := envSliceToMap(buildSoakServerEnv(18080, "/tmp/backflow-soak.db", "backflow-fake-agent:test"))

	if env["BACKFLOW_DATABASE_PATH"] != "/tmp/backflow-soak.db" {
		t.Fatalf("BACKFLOW_DATABASE_PATH = %q, want /tmp/backflow-soak.db", env["BACKFLOW_DATABASE_PATH"])
	}
	if env["BACKFLOW_LISTEN_ADDR"] != ":18080" {
		t.Fatalf("BACKFLOW_LISTEN_ADDR = %q, want :18080", env["BACKFLOW_LISTEN_ADDR"])
	}
	if env["BACKFLOW_AGENT_IMAGE"] != "backflow-fake-agent:test" {
		t.Fatalf("BACKFLOW_AGENT_IMAGE = %q, want backflow-fake-agent:test", env["BACKFLOW_AGENT_IMAGE"])
	}
	if env["BACKFLOW_API_KEY"] != "" {
		t.Fatalf("BACKFLOW_API_KEY = %q, want empty", env["BACKFLOW_API_KEY"])
	}
	if env["BACKFLOW_WEBHOOK_URL"] != "" {
		t.Fatalf("BACKFLOW_WEBHOOK_URL = %q, want empty", env["BACKFLOW_WEBHOOK_URL"])
	}
	if env["BACKFLOW_WEBHOOK_EVENTS"] != "" {
		t.Fatalf("BACKFLOW_WEBHOOK_EVENTS = %q, want empty", env["BACKFLOW_WEBHOOK_EVENTS"])
	}
	if env["BACKFLOW_DEFAULT_CREATE_PR"] != "false" {
		t.Fatalf("BACKFLOW_DEFAULT_CREATE_PR = %q, want false", env["BACKFLOW_DEFAULT_CREATE_PR"])
	}
	if env["BACKFLOW_DEFAULT_SELF_REVIEW"] != "false" {
		t.Fatalf("BACKFLOW_DEFAULT_SELF_REVIEW = %q, want false", env["BACKFLOW_DEFAULT_SELF_REVIEW"])
	}
	if env["BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT"] != "false" {
		t.Fatalf("BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT = %q, want false", env["BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT"])
	}
	if env["BACKFLOW_CONTAINERS_PER_INSTANCE"] != "4" {
		t.Fatalf("BACKFLOW_CONTAINERS_PER_INSTANCE = %q, want 4", env["BACKFLOW_CONTAINERS_PER_INSTANCE"])
	}
	if env["ANTHROPIC_API_KEY"] != "sk-test-fake" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want sk-test-fake", env["ANTHROPIC_API_KEY"])
	}
}

func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			out[parts[0]] = parts[1]
		}
	}
	return out
}
