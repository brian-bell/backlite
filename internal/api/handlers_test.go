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
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

type noopLogFetcher struct{}

func (noopLogFetcher) GetLogs(_ context.Context, _, _ string, _ int) (string, error) {
	return "test logs\n", nil
}

type noopEmitter struct{}

func (noopEmitter) Emit(_ notify.Event) {}

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

	pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
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
	if _, err := truncatePool.Exec(ctx, "TRUNCATE tasks, instances, allowed_senders CASCADE"); err != nil {
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

	if _, err := truncatePool.Exec(ctx, "TRUNCATE tasks, instances, allowed_senders CASCADE"); err != nil {
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

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCreateAndGetTask(t *testing.T) {
	srv := testServer(t)

	body := `{"repo_url":"https://github.com/test/repo","prompt":"Fix the bug","create_pr":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

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

	if w2.Code != http.StatusOK {
		t.Errorf("get status = %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestListTasks(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

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

	body := `{"repo_url":"https://github.com/test/repo","prompt":"Fix the bug","harness":"codex"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

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

	body := `{"repo_url":"https://github.com/test/repo","prompt":"Fix the bug","harness":"invalid"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCreateTaskValidation(t *testing.T) {
	srv := testServer(t)

	body := `{"repo_url":"","prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestCreateReviewTask(t *testing.T) {
	srv := testServer(t)

	body := `{"task_mode":"review","review_pr_url":"https://github.com/test/repo/pull/42","prompt":"Focus on security"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID             string `json:"id"`
			TaskMode       string `json:"task_mode"`
			ReviewPRURL    string `json:"review_pr_url"`
			ReviewPRNumber int    `json:"review_pr_number"`
			RepoURL        string `json:"repo_url"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.TaskMode != "review" {
		t.Errorf("task_mode = %q, want review", resp.Data.TaskMode)
	}
	if resp.Data.ReviewPRURL != "https://github.com/test/repo/pull/42" {
		t.Errorf("review_pr_url = %q, want https://github.com/test/repo/pull/42", resp.Data.ReviewPRURL)
	}
	if resp.Data.ReviewPRNumber != 42 {
		t.Errorf("review_pr_number = %d, want 42", resp.Data.ReviewPRNumber)
	}
	if resp.Data.RepoURL != "https://github.com/test/repo" {
		t.Errorf("repo_url = %q, want https://github.com/test/repo", resp.Data.RepoURL)
	}
}

func TestCreateReviewTaskBackwardCompat(t *testing.T) {
	srv := testServer(t)

	body := `{"task_mode":"review","repo_url":"https://github.com/test/repo","review_pr_number":42}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp struct {
		Data struct {
			ReviewPRURL string `json:"review_pr_url"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.ReviewPRURL != "https://github.com/test/repo/pull/42" {
		t.Errorf("review_pr_url = %q, want https://github.com/test/repo/pull/42", resp.Data.ReviewPRURL)
	}
}

func TestCreateReviewTaskMissingPR(t *testing.T) {
	srv := testServer(t)

	body := `{"task_mode":"review","repo_url":"https://github.com/test/repo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestDeleteTask(t *testing.T) {
	srv := testServer(t)

	// Create a task first
	body := `{"repo_url":"https://github.com/test/repo","prompt":"Fix it"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

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

	if w2.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want %d", w2.Code, http.StatusNoContent)
	}
}

func TestDeleteTask_EmitsCancelledEvent(t *testing.T) {
	srv, s, emitter := testServerWithEmitter(t)
	ctx := context.Background()

	// Create a task via API
	body := `{"repo_url":"https://github.com/test/repo","prompt":"Fix it"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

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
