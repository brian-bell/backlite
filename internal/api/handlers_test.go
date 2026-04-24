package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

func newTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), strings.ReplaceAll(t.Name(), "/", "-")+"-test.db")
	s, err := store.NewSQLite(context.Background(), dbPath, filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return s
}

func testServer(t *testing.T) http.Handler {
	t.Helper()
	s := newTestStore(t)

	cfg := &config.Config{
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
	s := newTestStore(t)

	cfg := &config.Config{
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

func TestCreateReadTask_ViaPOSTTasks(t *testing.T) {
	s := newTestStore(t)

	cfg := &config.Config{
		AnthropicAPIKey:       "sk-test",
		ReaderImage:           "backlite-reader:v1",
		DefaultHarness:        "claude_code",
		DefaultClaudeModel:    "claude-sonnet-4-6",
		DefaultCodexModel:     "gpt-5.4",
		DefaultEffort:         "medium",
		DefaultMaxBudget:      10.0,
		DefaultMaxRuntime:     30 * 60e9,
		DefaultMaxTurns:       200,
		DefaultReadMaxBudget:  0.5,
		DefaultReadMaxRuntime: 300 * 1e9,
		DefaultReadMaxTurns:   20,
	}
	srv := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	body := `{"prompt":"https://example.com/article","task_mode":"read","force":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			ID         string `json:"id"`
			TaskMode   string `json:"task_mode"`
			Force      bool   `json:"force"`
			AgentImage string `json:"agent_image"`
			CreatePR   bool   `json:"create_pr"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.TaskMode != "read" {
		t.Errorf("task_mode = %q, want read", resp.Data.TaskMode)
	}
	if !resp.Data.Force {
		t.Error("force = false, want true")
	}
	if resp.Data.AgentImage != "backlite-reader:v1" {
		t.Errorf("agent_image = %q, want reader image", resp.Data.AgentImage)
	}
	if resp.Data.CreatePR {
		t.Error("create_pr = true, want false for read mode")
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
	s := newTestStore(t)

	cfg := &config.Config{
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

// TestLookupReading_EmptyResultReturnsDataArray locks in the JSON envelope
// shape on a miss. The reader container's read-lookup.sh requires
// `.data | type == "array"`; if `data` is ever omitted on empty results the
// script fails with "unexpected response from Backlite API".
func TestLookupReading_EmptyResultReturnsDataArray(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/readings/lookup?url="+url.QueryEscape("https://example.com/never-captured"),
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw: %s)", err, rr.Body.String())
	}
	raw, ok := body["data"]
	if !ok {
		t.Fatalf("response missing \"data\" key: %s", rr.Body.String())
	}
	if string(raw) != "[]" {
		t.Fatalf("data = %s, want []", string(raw))
	}
}

// TestFindSimilarReadings_EmptyResultReturnsDataArray mirrors the lookup test
// for the POST similarity endpoint.
func TestFindSimilarReadings_EmptyResultReturnsDataArray(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/readings/similar",
		strings.NewReader(`{"query_embedding":[1,0,0],"match_count":3}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw: %s)", err, rr.Body.String())
	}
	raw, ok := body["data"]
	if !ok {
		t.Fatalf("response missing \"data\" key: %s", rr.Body.String())
	}
	if string(raw) != "[]" {
		t.Fatalf("data = %s, want []", string(raw))
	}
}
