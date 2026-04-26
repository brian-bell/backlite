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
	"time"

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
	s.StartTask(ctx, taskID, "container-abc", "")

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

func TestListReadings_ReturnsNewestFirstPage(t *testing.T) {
	srv, s, _ := testServerWithEmitter(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	task := &models.Task{
		ID:        "bf_READ_TASK",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/source",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	for _, seed := range []struct {
		id        string
		url       string
		title     string
		createdAt time.Time
	}{
		{"bf_READ_OLD", "https://example.com/old", "Old", now},
		{"bf_READ_NEW", "https://example.com/new", "New", now.Add(2 * time.Hour)},
		{"bf_READ_MID", "https://example.com/mid", "Middle", now.Add(time.Hour)},
	} {
		r := &models.Reading{
			ID:             seed.id,
			TaskID:         task.ID,
			URL:            seed.url,
			Title:          seed.title,
			TLDR:           seed.title + " tldr",
			Tags:           []string{"research"},
			Keywords:       []string{},
			People:         []string{},
			Orgs:           []string{},
			NoveltyVerdict: "new",
			Connections:    []models.Connection{},
			Summary:        seed.title + " summary",
			RawOutput:      []byte(`{}`),
			Embedding:      []float32{1, 0, 0},
			CreatedAt:      seed.createdAt,
		}
		if err := s.UpsertReading(ctx, r); err != nil {
			t.Fatalf("UpsertReading %s: %v", seed.id, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings?limit=1&offset=1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	bodyBytes := w.Body.Bytes()

	var resp struct {
		Data struct {
			Readings []models.Reading `json:"readings"`
			Limit    int              `json:"limit"`
			Offset   int              `json:"offset"`
			HasMore  bool             `json:"has_more"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(bodyBytes)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Limit != 1 || resp.Data.Offset != 1 {
		t.Fatalf("pagination = limit %d offset %d, want limit 1 offset 1", resp.Data.Limit, resp.Data.Offset)
	}
	if !resp.Data.HasMore {
		t.Fatal("has_more = false, want true")
	}
	if len(resp.Data.Readings) != 1 {
		t.Fatalf("got %d readings, want 1", len(resp.Data.Readings))
	}
	if resp.Data.Readings[0].ID != "bf_READ_MID" {
		t.Fatalf("reading id = %q, want bf_READ_MID", resp.Data.Readings[0].ID)
	}
	if resp.Data.Readings[0].Embedding != nil {
		t.Fatalf("embedding = %v, want nil", resp.Data.Readings[0].Embedding)
	}
	if strings.Contains(string(bodyBytes), "raw_output") {
		t.Fatalf("list response exposed raw_output: %s", string(bodyBytes))
	}
	if strings.Contains(string(bodyBytes), "embedding") {
		t.Fatalf("list response exposed embedding: %s", string(bodyBytes))
	}
}

func TestGetReading_ReturnsDetail(t *testing.T) {
	srv, s, _ := testServerWithEmitter(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	task := &models.Task{
		ID:        "bf_READ_DETAIL_TASK",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/detail",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	reading := &models.Reading{
		ID:             "bf_READ_DETAIL",
		TaskID:         task.ID,
		URL:            "https://example.com/detail",
		Title:          "Detailed Reading",
		TLDR:           "Short version",
		Tags:           []string{"backend"},
		Keywords:       []string{"sqlite"},
		People:         []string{"Ada"},
		Orgs:           []string{"Backlite"},
		NoveltyVerdict: "new",
		Connections:    []models.Connection{{ReadingID: "bf_READ_OTHER", Reason: "same topic"}},
		Summary:        "The full summary.",
		RawOutput:      []byte(`{"internal":true}`),
		Embedding:      []float32{0.1, 0.9},
		CreatedAt:      now,
	}
	if err := s.UpsertReading(ctx, reading); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+reading.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	bodyBytes := w.Body.Bytes()

	var resp struct {
		Data models.Reading `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(bodyBytes)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.ID != reading.ID || resp.Data.URL != reading.URL || resp.Data.Summary != reading.Summary {
		t.Fatalf("reading detail = %#v, want seeded detail", resp.Data)
	}
	if len(resp.Data.Keywords) != 1 || resp.Data.Keywords[0] != "sqlite" {
		t.Fatalf("keywords = %v, want [sqlite]", resp.Data.Keywords)
	}
	if len(resp.Data.People) != 1 || resp.Data.People[0] != "Ada" {
		t.Fatalf("people = %v, want [Ada]", resp.Data.People)
	}
	if resp.Data.Embedding != nil {
		t.Fatalf("embedding = %v, want nil", resp.Data.Embedding)
	}
	if strings.Contains(string(bodyBytes), "raw_output") {
		t.Fatalf("detail response exposed raw_output: %s", string(bodyBytes))
	}
	if strings.Contains(string(bodyBytes), "embedding") {
		t.Fatalf("detail response exposed embedding: %s", string(bodyBytes))
	}
}

func TestGetReading_NormalizesNilCollections(t *testing.T) {
	srv, s, _ := testServerWithEmitter(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	task := &models.Task{
		ID:        "bf_READ_NIL_TASK",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/nil",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	reading := &models.Reading{
		ID:             "bf_READ_NIL",
		TaskID:         task.ID,
		URL:            "https://example.com/nil",
		Title:          "Nil Collections",
		TLDR:           "Short version",
		NoveltyVerdict: "new",
		Summary:        "The full summary.",
		RawOutput:      []byte(`{"internal":true}`),
		CreatedAt:      now,
	}
	if err := s.UpsertReading(ctx, reading); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+reading.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	bodyBytes := w.Body.Bytes()
	var resp struct {
		Data struct {
			Tags        []string            `json:"tags"`
			Keywords    []string            `json:"keywords"`
			People      []string            `json:"people"`
			Orgs        []string            `json:"orgs"`
			Connections []models.Connection `json:"connections"`
		} `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(bodyBytes)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Tags == nil || resp.Data.Keywords == nil || resp.Data.People == nil || resp.Data.Orgs == nil || resp.Data.Connections == nil {
		t.Fatalf("collections should decode as empty arrays, got body: %s", string(bodyBytes))
	}
}

func TestListReadings_FiltersByQueryAcrossSearchableFields(t *testing.T) {
	srv, s, _ := testServerWithEmitter(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	task := &models.Task{
		ID:        "bf_READ_Q_TASK",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/q",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	seeds := []struct {
		id      string
		url     string
		title   string
		tldr    string
		summary string
	}{
		{"bf_READ_Q_TITLE", "https://example.com/a", "SQLite Internals", "no match here", "different body"},
		{"bf_READ_Q_URL", "https://example.com/sqlite-tips", "Other", "no match here", "different body"},
		{"bf_READ_Q_TLDR", "https://example.com/c", "Other", "Mostly about SQLite under the hood", "different body"},
		{"bf_READ_Q_SUMMARY", "https://example.com/d", "Other", "no match here", "Deep dive into sqlite query planning"},
		{"bf_READ_Q_NONE", "https://example.com/e", "Postgres", "MVCC overview", "Postgres write-ahead log"},
	}
	for i, seed := range seeds {
		r := &models.Reading{
			ID:             seed.id,
			TaskID:         task.ID,
			URL:            seed.url,
			Title:          seed.title,
			TLDR:           seed.tldr,
			Tags:           []string{},
			Keywords:       []string{},
			People:         []string{},
			Orgs:           []string{},
			NoveltyVerdict: "new",
			Connections:    []models.Connection{},
			Summary:        seed.summary,
			RawOutput:      []byte(`{}`),
			CreatedAt:      now.Add(time.Duration(i) * time.Minute),
		}
		if err := s.UpsertReading(ctx, r); err != nil {
			t.Fatalf("UpsertReading %s: %v", seed.id, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings?q=sqlite", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp struct {
		Data struct {
			Readings []models.Reading `json:"readings"`
			HasMore  bool             `json:"has_more"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := make(map[string]bool, len(resp.Data.Readings))
	for _, r := range resp.Data.Readings {
		got[r.ID] = true
	}
	wantHits := []string{"bf_READ_Q_TITLE", "bf_READ_Q_URL", "bf_READ_Q_TLDR", "bf_READ_Q_SUMMARY"}
	for _, id := range wantHits {
		if !got[id] {
			t.Errorf("missing match for %s; got ids %v", id, keys(got))
		}
	}
	if got["bf_READ_Q_NONE"] {
		t.Errorf("non-matching reading returned; got ids %v", keys(got))
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestListReadings_FiltersByTagAndCombinesWithQuery(t *testing.T) {
	srv, s, _ := testServerWithEmitter(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	task := &models.Task{
		ID:        "bf_READ_TAG_TASK",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/tag",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	seeds := []struct {
		id    string
		title string
		tags  []string
	}{
		{"bf_READ_TAG_GO", "Go SQLite Tips", []string{"go", "sqlite"}},
		{"bf_READ_TAG_RUST", "Rust SQLite Tips", []string{"rust", "sqlite"}},
		{"bf_READ_TAG_GO_OTHER", "Go Concurrency", []string{"go", "concurrency"}},
		{"bf_READ_TAG_PG", "Postgres MVCC", []string{"postgres"}},
	}
	for i, seed := range seeds {
		r := &models.Reading{
			ID:             seed.id,
			TaskID:         task.ID,
			URL:            "https://example.com/" + seed.id,
			Title:          seed.title,
			TLDR:           seed.title + " tldr",
			Tags:           seed.tags,
			Keywords:       []string{},
			People:         []string{},
			Orgs:           []string{},
			NoveltyVerdict: "new",
			Connections:    []models.Connection{},
			Summary:        seed.title + " summary",
			RawOutput:      []byte(`{}`),
			CreatedAt:      now.Add(time.Duration(i) * time.Minute),
		}
		if err := s.UpsertReading(ctx, r); err != nil {
			t.Fatalf("UpsertReading %s: %v", seed.id, err)
		}
	}

	// tag-only filter returns both go-tagged readings.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings?tag=go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp struct {
		Data struct {
			Readings []models.Reading `json:"readings"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := make(map[string]bool, len(resp.Data.Readings))
	for _, r := range resp.Data.Readings {
		got[r.ID] = true
	}
	for _, want := range []string{"bf_READ_TAG_GO", "bf_READ_TAG_GO_OTHER"} {
		if !got[want] {
			t.Errorf("missing %s for tag=go; got %v", want, keys(got))
		}
	}
	for _, unwanted := range []string{"bf_READ_TAG_RUST", "bf_READ_TAG_PG"} {
		if got[unwanted] {
			t.Errorf("unexpected %s in tag=go results; got %v", unwanted, keys(got))
		}
	}

	// q + tag combine: only Go readings whose title mentions sqlite.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/readings?tag=go&q=sqlite", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	checkResponse(t, req2, w2)
	if w2.Code != http.StatusOK {
		t.Fatalf("combined filter status = %d, body: %s", w2.Code, w2.Body.String())
	}
	var resp2 struct {
		Data struct {
			Readings []models.Reading `json:"readings"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode combined: %v", err)
	}
	if len(resp2.Data.Readings) != 1 || resp2.Data.Readings[0].ID != "bf_READ_TAG_GO" {
		ids := make([]string, 0, len(resp2.Data.Readings))
		for _, r := range resp2.Data.Readings {
			ids = append(ids, r.ID)
		}
		t.Fatalf("combined filter returned %v, want [bf_READ_TAG_GO]", ids)
	}
}

func TestGetReading_IncludesResolvedRelatedAndOriginatingTask(t *testing.T) {
	srv, s, _ := testServerWithEmitter(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	originatingTask := &models.Task{
		ID:        "bf_TASK_PARENT",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://example.com/article",
		Prompt:    "https://example.com/article",
		OutputURL: "/api/v1/tasks/bf_TASK_PARENT/output",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, originatingTask); err != nil {
		t.Fatalf("CreateTask parent: %v", err)
	}

	relatedTask := &models.Task{
		ID:        "bf_TASK_RELATED",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/related",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, relatedTask); err != nil {
		t.Fatalf("CreateTask related: %v", err)
	}

	relatedReading := &models.Reading{
		ID:             "bf_READ_RELATED",
		TaskID:         relatedTask.ID,
		URL:            "https://example.com/related",
		Title:          "Related Reading Title",
		TLDR:           "Related TL;DR",
		Tags:           []string{"backend"},
		Keywords:       []string{},
		People:         []string{},
		Orgs:           []string{},
		NoveltyVerdict: "new",
		Connections:    []models.Connection{},
		Summary:        "Related summary",
		RawOutput:      []byte(`{}`),
		CreatedAt:      now,
	}
	if err := s.UpsertReading(ctx, relatedReading); err != nil {
		t.Fatalf("UpsertReading related: %v", err)
	}

	subjectReading := &models.Reading{
		ID:             "bf_READ_SUBJECT",
		TaskID:         originatingTask.ID,
		URL:            "https://example.com/article",
		Title:          "Subject",
		TLDR:           "Subject tldr",
		Tags:           []string{"backend"},
		Keywords:       []string{},
		People:         []string{},
		Orgs:           []string{},
		NoveltyVerdict: "new",
		Connections: []models.Connection{
			{ReadingID: "bf_READ_RELATED", Reason: "covers same topic"},
			{ReadingID: "bf_READ_DANGLING", Reason: "no longer exists"},
		},
		Summary:   "Subject summary",
		RawOutput: []byte(`{}`),
		CreatedAt: now,
	}
	if err := s.UpsertReading(ctx, subjectReading); err != nil {
		t.Fatalf("UpsertReading subject: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+subjectReading.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	type relatedItem struct {
		ReadingID      string `json:"reading_id"`
		Reason         string `json:"reason"`
		Title          string `json:"title"`
		TLDR           string `json:"tldr"`
		URL            string `json:"url"`
		NoveltyVerdict string `json:"novelty_verdict"`
	}
	type originatingTaskInfo struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		RepoURL   string `json:"repo_url"`
		PRURL     string `json:"pr_url"`
		OutputURL string `json:"output_url"`
		Error     string `json:"error"`
	}
	var resp struct {
		Data struct {
			ID              string               `json:"id"`
			Related         []relatedItem        `json:"related"`
			OriginatingTask *originatingTaskInfo `json:"originating_task"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(resp.Data.Related) != 1 {
		t.Fatalf("related = %#v, want exactly the resolvable connection", resp.Data.Related)
	}
	got := resp.Data.Related[0]
	if got.ReadingID != "bf_READ_RELATED" || got.Title != "Related Reading Title" ||
		got.TLDR != "Related TL;DR" || got.URL != "https://example.com/related" ||
		got.Reason != "covers same topic" {
		t.Fatalf("related[0] = %#v, want resolved reading data + reason", got)
	}

	if resp.Data.OriginatingTask == nil {
		t.Fatalf("originating_task = nil, want resolved task")
	}
	if resp.Data.OriginatingTask.ID != originatingTask.ID || resp.Data.OriginatingTask.Status != string(models.TaskStatusCompleted) {
		t.Fatalf("originating_task = %#v, want id %q status completed", resp.Data.OriginatingTask, originatingTask.ID)
	}
	if resp.Data.OriginatingTask.OutputURL != originatingTask.OutputURL {
		t.Fatalf("originating_task.output_url = %q, want %q", resp.Data.OriginatingTask.OutputURL, originatingTask.OutputURL)
	}
}

func TestGetReading_NoConnectionsAndAllDanglingReturnsEmptyRelated(t *testing.T) {
	srv, s, _ := testServerWithEmitter(t)
	ctx := context.Background()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	task := &models.Task{
		ID:        "bf_TASK_NO_CONN",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/no-conn",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	reading := &models.Reading{
		ID:             "bf_READ_NO_CONN",
		TaskID:         task.ID,
		URL:            "https://example.com/no-conn",
		Title:          "No connections",
		TLDR:           "no related",
		Tags:           []string{},
		Keywords:       []string{},
		People:         []string{},
		Orgs:           []string{},
		NoveltyVerdict: "new",
		// One connection that points at a never-stored reading; should be
		// dropped from the resolved related[] payload.
		Connections: []models.Connection{
			{ReadingID: "bf_READ_DANGLING", Reason: "dropped"},
		},
		Summary:   "no connections summary",
		RawOutput: []byte(`{}`),
		CreatedAt: now,
	}
	if err := s.UpsertReading(ctx, reading); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+reading.ID, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	checkResponse(t, req, w)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body: %s", w.Code, w.Body.String())
	}

	bodyBytes := w.Body.Bytes()
	if !bytes.Contains(bodyBytes, []byte(`"related":[]`)) {
		t.Fatalf("expected related to be an empty array; body: %s", string(bodyBytes))
	}
	if !bytes.Contains(bodyBytes, []byte(`"originating_task":`)) {
		t.Fatalf("expected originating_task key in body: %s", string(bodyBytes))
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
