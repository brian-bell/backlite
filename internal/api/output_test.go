//go:build !nocontainers

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/store"
)

// outputTestServer creates a server rooted at a temp DataDir and returns
// both the handler and the data dir so tests can seed output files.
func outputTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	ctx := context.Background()

	if _, err := truncatePool.Exec(ctx, "TRUNCATE tasks, instances, allowed_senders, api_keys CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

	s, err := store.NewPostgres(ctx, sharedConnStr, migrationsDir)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	dataDir := t.TempDir()

	cfg := &config.Config{
		AnthropicAPIKey:    "sk-test",
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultCodexModel:  "gpt-5.4",
		DefaultEffort:      "medium",
		DefaultMaxBudget:   10.0,
		DefaultMaxRuntime:  30 * 60e9,
		DefaultMaxTurns:    200,
		DataDir:            dataDir,
	}

	return NewServer(s, cfg, noopLogFetcher{}, noopEmitter{}), dataDir
}

func writeOutputFiles(t *testing.T, dataDir, taskID, logContent, jsonContent string) {
	t.Helper()
	dir := filepath.Join(dataDir, "tasks", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "container_output.log"), []byte(logContent), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.json"), []byte(jsonContent), 0o644); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func TestGetTaskOutput_200(t *testing.T) {
	srv, dataDir := outputTestServer(t)

	// Create a real task (so /output can look it up) via the API.
	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())

	writeOutputFiles(t, dataDir, id, "log content here", `{"id":"`+id+`"}`)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/output", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /output status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "log content here" {
		t.Errorf("body = %q, want %q", got, "log content here")
	}
}

func TestGetTaskOutputJSON_200(t *testing.T) {
	srv, dataDir := outputTestServer(t)

	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())

	writeOutputFiles(t, dataDir, id, "log", `{"id":"`+id+`","status":"completed"}`)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/output.json", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /output.json status = %d, want 200", rec.Code)
	}
	var decoded map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal body: %v (body: %s)", err, rec.Body.String())
	}
	if decoded["status"] != "completed" {
		t.Errorf("status field = %q, want %q", decoded["status"], "completed")
	}
}

func TestGetTaskOutput_404_WhenFileMissing(t *testing.T) {
	srv, _ := outputTestServer(t)

	// Valid-looking task ID, but no file on disk.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/bf_01HX000000000000000000MISSG/output", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestGetTaskOutput_401_WhenBearerMissing(t *testing.T) {
	ctx := context.Background()
	if _, err := truncatePool.Exec(ctx, "TRUNCATE tasks, instances, allowed_senders, api_keys CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	s, err := store.NewPostgres(ctx, sharedConnStr, migrationsDir)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		AnthropicAPIKey: "sk-test",
		APIKey:          "secret-token",
		DataDir:         t.TempDir(),
	}
	srv := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/bf_01HX000000000000000000DEADX/output", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without bearer token", rec.Code)
	}
}
