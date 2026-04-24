package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/store"
)

// outputTestServer creates a server rooted at a temp DataDir and returns
// both the handler and the data dir so tests can seed output files.
func outputTestServer(t *testing.T) (http.Handler, *store.SQLiteStore, string) {
	t.Helper()
	s := newTestStore(t)

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
		MaxUserRetries:     2,
		DataDir:            dataDir,
	}

	return NewServer(s, cfg, noopLogFetcher{}, noopEmitter{}), s, dataDir
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
	srv, s, dataDir := outputTestServer(t)
	ctx := context.Background()

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
	if err := s.CompleteTask(ctx, id, store.TaskResult{
		Status:    models.TaskStatusCompleted,
		OutputURL: "/api/v1/tasks/" + id + "/output",
	}); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

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
	srv, s, dataDir := outputTestServer(t)
	ctx := context.Background()

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
	if err := s.CompleteTask(ctx, id, store.TaskResult{
		Status:    models.TaskStatusCompleted,
		OutputURL: "/api/v1/tasks/" + id + "/output",
	}); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

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
	srv, _, _ := outputTestServer(t)

	// Valid-looking task ID, but no file on disk.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/bf_01HX00000000000000000MISSG/output", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestGetTaskOutput_400_RejectsMalformedTaskID(t *testing.T) {
	srv, _, dataDir := outputTestServer(t)

	// Seed a file outside the per-task tree. If the handler allowed traversal,
	// a crafted ID could stat this file and succeed.
	outside := filepath.Join(dataDir, "secrets.txt")
	if err := os.WriteFile(outside, []byte("top-secret"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	// Each of these IDs fails the ^bf_[0-9A-Z]{26}$ pattern and must be
	// rejected with 400 before any filesystem access happens.
	bad := []string{
		"..",
		"../secrets",
		"bf_..",
		"bf_01HX0000000000000000000MISS", // wrong length
		"bf_01HX00000000000000000miss!",  // bad chars
		"bf_01HX00000000000000000MISSG/../secrets",
	}

	for _, id := range bad {
		t.Run(id, func(t *testing.T) {
			// URL-escape to keep chi's router happy; the handler still sees the
			// decoded id via chi.URLParam.
			req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+url.PathEscape(id)+"/output", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d for id %q, want 400 or 404 (body: %s)", rec.Code, id, rec.Body.String())
			}
			// In no case should the response body contain the seeded file's bytes.
			if bytes.Contains(rec.Body.Bytes(), []byte("top-secret")) {
				t.Fatalf("response leaked seeded file bytes for id %q: %s", id, rec.Body.String())
			}
		})
	}
}

func TestGetTaskOutput_401_WhenBearerMissing(t *testing.T) {
	s := newTestStore(t)

	cfg := &config.Config{
		AnthropicAPIKey: "sk-test",
		APIKey:          "secret-token",
		DataDir:         t.TempDir(),
	}
	srv := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/bf_01HX00000000000000000DEADX/output", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without bearer token", rec.Code)
	}
}

func TestGetTaskOutput_404_AfterRetryWhileCurrentAttemptPending(t *testing.T) {
	srv, s, dataDir := outputTestServer(t)
	ctx := context.Background()

	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())

	writeOutputFiles(t, dataDir, id, "previous attempt log", `{"id":"`+id+`","status":"failed"}`)

	if err := s.CompleteTask(ctx, id, store.TaskResult{
		Status:    models.TaskStatusFailed,
		OutputURL: "/api/v1/tasks/" + id + "/output",
		Error:     "previous attempt failed",
	}); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.MarkReadyForRetry(ctx, id); err != nil {
		t.Fatalf("MarkReadyForRetry: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id+"/retry", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /retry status = %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/output", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /output after retry status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestGetTaskOutput_404_AfterRetryWhenNewAttemptCancelsBeforeProducingOutput(t *testing.T) {
	srv, s, dataDir := outputTestServer(t)
	ctx := context.Background()

	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())

	writeOutputFiles(t, dataDir, id, "previous attempt log", `{"id":"`+id+`","status":"failed"}`)

	if err := s.CompleteTask(ctx, id, store.TaskResult{
		Status:    models.TaskStatusFailed,
		OutputURL: "/api/v1/tasks/" + id + "/output",
		Error:     "previous attempt failed",
	}); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.MarkReadyForRetry(ctx, id); err != nil {
		t.Fatalf("MarkReadyForRetry: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id+"/retry", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /retry status = %d: %s", rec.Code, rec.Body.String())
	}

	if err := s.CancelTask(ctx, id); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/output", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /output after cancelling retried task status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestGetTaskOutput_RetryDoesNotLeakPreviousAttemptFile is a regression anchor
// for the retry output-gating invariant: /output and /output.json are gated by
// current-attempt DB state (terminal + non-empty output_url), not by raw file
// presence. The per-task filesystem path is reused across attempts, so if a
// future handler ever drops the DB gate and checks only the file, previous
// attempts' logs will leak.
//
// This test explicitly verifies both halves of the invariant: (a) the stale
// bytes are still on disk (so the 404 isn't just "file missing"), and (b) the
// handler returns 404 anyway because RetryTask cleared output_url.
func TestGetTaskOutput_RetryDoesNotLeakPreviousAttemptFile(t *testing.T) {
	srv, s, dataDir := outputTestServer(t)
	ctx := context.Background()

	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())

	const staleLog = "previous attempt log bytes that must not leak"
	const staleJSON = `{"id":"` + "stale" + `","status":"failed"}`
	writeOutputFiles(t, dataDir, id, staleLog, staleJSON)

	if err := s.CompleteTask(ctx, id, store.TaskResult{
		Status:    models.TaskStatusFailed,
		OutputURL: "/api/v1/tasks/" + id + "/output",
		Error:     "previous attempt failed",
	}); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.MarkReadyForRetry(ctx, id); err != nil {
		t.Fatalf("MarkReadyForRetry: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id+"/retry", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /retry status = %d: %s", rec.Code, rec.Body.String())
	}

	// Invariant half (a): the per-attempt cleanup only clears output_url in the
	// DB; the filesystem path is per-task and is NOT deleted on retry. If this
	// assertion ever starts failing we'd be testing a different invariant.
	logBytes, err := os.ReadFile(filepath.Join(dataDir, "tasks", id, "container_output.log"))
	if err != nil {
		t.Fatalf("stale log file should still exist on disk after retry: %v", err)
	}
	if string(logBytes) != staleLog {
		t.Fatalf("stale log content = %q, want %q (test setup assumption invalid)", string(logBytes), staleLog)
	}

	// Invariant half (b): handler must still refuse to serve those bytes.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/output", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /output after retry status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(staleLog)) {
		t.Fatalf("response leaked previous attempt log bytes: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/output.json", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /output.json after retry status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"status":"failed"`)) {
		t.Fatalf("response leaked previous attempt task.json: %s", rec.Body.String())
	}
}

func TestGetTaskOutputJSON_404_AfterRetryWhileCurrentAttemptPending(t *testing.T) {
	srv, s, dataDir := outputTestServer(t)
	ctx := context.Background()

	body := `{"prompt":"Fix the bug in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	id := extractID(t, rec.Body.Bytes())

	writeOutputFiles(t, dataDir, id, "previous attempt log", `{"id":"`+id+`","status":"failed"}`)

	if err := s.CompleteTask(ctx, id, store.TaskResult{
		Status:    models.TaskStatusFailed,
		OutputURL: "/api/v1/tasks/" + id + "/output",
		Error:     "previous attempt failed",
	}); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := s.MarkReadyForRetry(ctx, id); err != nil {
		t.Fatalf("MarkReadyForRetry: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+id+"/retry", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /retry status = %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+id+"/output.json", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /output.json after retry status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}
