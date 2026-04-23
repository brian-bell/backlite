package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/backflow-labs/backflow/internal/store"
)

func TestTruncateTasks_ClearsCurrentSchema(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "soak-test.db")

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
		INSERT INTO instances (instance_id, instance_type, status, created_at, updated_at)
		VALUES ('local', 'local', 'running', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
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
