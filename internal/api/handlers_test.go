//go:build !nocontainers

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

type capturingEmitter struct {
	events []notify.Event
}

func (c *capturingEmitter) Emit(e notify.Event) { c.events = append(c.events, e) }

var (
	sharedConnStr string
	truncatePool  *pgxpool.Pool
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx, "pgvector/pgvector:pg16",
		postgres.WithDatabase("backflow_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		log.Fatalf("start postgres container: %v", err)
	}

	sharedConnStr, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get connection string: %v", err)
	}

	// Run migrations once.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	s, err := store.NewPostgres(ctx, sharedConnStr, migrationsDir)
	if err != nil {
		log.Fatalf("NewPostgres: %v", err)
	}
	s.Close()

	truncatePool, err = pgxpool.New(ctx, sharedConnStr)
	if err != nil {
		log.Fatalf("truncate pool: %v", err)
	}

	code := m.Run()

	truncatePool.Close()
	pgContainer.Terminate(ctx)
	os.Exit(code)
}

func testServer(t *testing.T) http.Handler {
	t.Helper()
	ctx := context.Background()

	// Clean slate for test isolation.
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
		AuthMode:           config.AuthModeAPIKey,
		AnthropicAPIKey:    "sk-test",
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultCodexModel:  "gpt-5.4",
		DefaultEffort:      "medium",
		DefaultMaxBudget:   10.0,
		DefaultMaxRuntime:  30 * 60e9, // 30 min
		DefaultMaxTurns:    200,
	}

	return NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})
}

func testServerWithEmitter(t *testing.T) (http.Handler, store.Store, *capturingEmitter) {
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

	cfg := &config.Config{
		AuthMode:           config.AuthModeAPIKey,
		AnthropicAPIKey:    "sk-test",
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultCodexModel:  "gpt-5.4",
		DefaultEffort:      "medium",
		DefaultMaxBudget:   10.0,
		DefaultMaxRuntime:  30 * 60e9,
		DefaultMaxTurns:    200,
	}

	emitter := &capturingEmitter{}
	return NewServer(s, cfg, noopLogFetcher{}, emitter), s, emitter
}

func TestHealthCheck(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCreateAndGetTask(t *testing.T) {
	srv := testServer(t)

	body := `{"prompt":"fix bug in https://github.com/test/repo","create_pr":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.ID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if resp.Data.Status != "pending" {
		t.Errorf("status = %q, want pending", resp.Data.Status)
	}

	// Get the task
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+resp.Data.ID, nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	checkResponse(t, req2, w2)

	if w2.Code != http.StatusOK {
		t.Errorf("get status = %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestListTasks(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Data []any `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data == nil {
		t.Error("expected non-nil data array")
	}
}

func TestCreateTaskCodexHarness(t *testing.T) {
	srv := testServer(t)

	body := `{"prompt":"fix bug in https://github.com/test/repo","harness":"codex"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID      string `json:"id"`
			Harness string `json:"harness"`
			Model   string `json:"model"`
			Effort  string `json:"effort"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.Harness != "codex" {
		t.Errorf("harness = %q, want codex", resp.Data.Harness)
	}
	if resp.Data.Model == "" {
		t.Error("model is empty, want non-empty default for codex harness")
	}
	if resp.Data.Effort != "medium" {
		t.Errorf("effort = %q, want medium", resp.Data.Effort)
	}
}

func TestCreateTaskInvalidHarness(t *testing.T) {
	srv := testServer(t)

	body := `{"prompt":"fix bug in https://github.com/test/repo","harness":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	srv := testServer(t)

	body := `{"prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCreateReviewTask(t *testing.T) {
	srv := testServer(t)

	body := `{"prompt":"Review https://github.com/test/repo/pull/42 focusing on security"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID       string `json:"id"`
			TaskMode string `json:"task_mode"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.TaskMode != "auto" {
		t.Errorf("task_mode = %q, want auto", resp.Data.TaskMode)
	}
}

func TestDeleteTask(t *testing.T) {
	srv := testServer(t)

	// Create a task first
	body := `{"prompt":"fix it in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	// Delete it
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+resp.Data.ID, nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	checkResponse(t, req2, w2)

	if w2.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want %d", w2.Code, http.StatusNoContent)
	}
}

func TestNewTask_Integration(t *testing.T) {
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
		AuthMode:           config.AuthModeAPIKey,
		AnthropicAPIKey:    "sk-test",
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultCodexModel:  "gpt-5.4",
		DefaultEffort:      "medium",
		DefaultMaxBudget:   10.0,
		DefaultMaxRuntime:  30 * 60e9,
		DefaultMaxTurns:    200,
	}

	emitter := &capturingEmitter{}

	req := &models.CreateTaskRequest{
		Prompt:  "Add tests for auth module",
		Harness: "claude_code",
	}

	task, err := NewTask(ctx, req, s, cfg, emitter)
	if err != nil {
		t.Fatalf("NewTask returned error: %v", err)
	}

	if task.ID == "" {
		t.Error("task ID is empty")
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.Prompt != "Add tests for auth module" {
		t.Errorf("prompt = %q", task.Prompt)
	}
	if task.Harness != "claude_code" {
		t.Errorf("harness = %q, want claude_code", task.Harness)
	}

	// Verify it was persisted to the store.
	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask(%s): %v", task.ID, err)
	}
	if got.Prompt != task.Prompt {
		t.Errorf("stored prompt = %q, want %q", got.Prompt, task.Prompt)
	}

	// Verify event was emitted.
	if len(emitter.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(emitter.events))
	}
	if emitter.events[0].Type != notify.EventTaskCreated {
		t.Errorf("event type = %q, want %q", emitter.events[0].Type, notify.EventTaskCreated)
	}
	if emitter.events[0].TaskID != task.ID {
		t.Errorf("event task_id = %q, want %q", emitter.events[0].TaskID, task.ID)
	}
}

func TestDeleteTask_EmitsCancelledEvent(t *testing.T) {
	srv, s, emitter := testServerWithEmitter(t)
	ctx := context.Background()

	// Create a task via API
	body := `{"prompt":"fix it in https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	taskID := resp.Data.ID

	// Transition task to running so cancel path is triggered
	s.StartTask(ctx, taskID, "container-abc")

	// Clear events from creation
	emitter.events = nil

	// Cancel it
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+taskID, nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	checkResponse(t, req2, w2)

	if w2.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", w2.Code, http.StatusNoContent)
	}

	// Verify a cancelled event was emitted
	if len(emitter.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(emitter.events))
	}
	if emitter.events[0].Type != notify.EventTaskCancelled {
		t.Fatalf("event type = %q, want %q", emitter.events[0].Type, notify.EventTaskCancelled)
	}
	if emitter.events[0].TaskID != taskID {
		t.Fatalf("event task_id = %q, want %q", emitter.events[0].TaskID, taskID)
	}
}
