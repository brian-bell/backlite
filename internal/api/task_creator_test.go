package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

// mockStore implements store.Store for unit tests that need a failing CreateTask.
type mockStore struct {
	store.Store
	createErr   error
	createCalls int
}

func (m *mockStore) CreateTask(_ context.Context, _ *models.Task) error {
	m.createCalls++
	return m.createErr
}

func (m *mockStore) HasAPIKeys(_ context.Context) (bool, error) {
	return false, nil
}

func (m *mockStore) GetAPIKeyByHash(_ context.Context, _ string) (*models.APIKey, error) {
	return nil, store.ErrNotFound
}

func (m *mockStore) CreateAPIKey(_ context.Context, _ *models.APIKey) error {
	return nil
}

func TestNewTask_StoreError_ReturnsErrStoreFailure(t *testing.T) {
	cfg := &config.Config{}
	s := &mockStore{createErr: fmt.Errorf("connection refused")}
	req := &models.CreateTaskRequest{
		Prompt: "Fix bug",
	}

	_, err := NewTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrStoreFailure) {
		t.Errorf("error = %v, want errors.Is(err, ErrStoreFailure)", err)
	}
}

func TestNewTask_ValidationError_NotStoreFailure(t *testing.T) {
	cfg := &config.Config{}
	s := &mockStore{}
	req := &models.CreateTaskRequest{
		Prompt: "",
	}

	_, err := NewTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrStoreFailure) {
		t.Errorf("validation error should not match ErrStoreFailure, got: %v", err)
	}
}

type capturingEmitter struct {
	events []notify.Event
}

func (c *capturingEmitter) Emit(e notify.Event) { c.events = append(c.events, e) }

// readTestConfig returns a config suitable for testing NewReadTask: populates
// the reader image and read-mode caps that TaskDefaults("read") reads from.
func readTestConfig() *config.Config {
	return &config.Config{
		AgentImage:            "backlite-agent",
		ReaderImage:           "backlite-reader:v1",
		DefaultHarness:        "claude_code",
		DefaultClaudeModel:    "claude-sonnet-4-6",
		DefaultCodexModel:     "gpt-5.4",
		DefaultEffort:         "medium",
		DefaultReadMaxBudget:  0.5,
		DefaultReadMaxRuntime: 300_000_000_000, // 300s in ns
		DefaultReadMaxTurns:   20,
		DefaultSaveOutput:     true,
	}
}

func TestNewReadTask_SetsReadModeAndReaderImage(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	req := &models.CreateTaskRequest{Prompt: "https://example.com/post"}

	task, err := NewReadTask(context.Background(), req, s, cfg, nil)
	if err != nil {
		t.Fatalf("NewReadTask: %v", err)
	}
	if task.TaskMode != models.TaskModeRead {
		t.Errorf("TaskMode = %q, want %q", task.TaskMode, models.TaskModeRead)
	}
	if task.AgentImage != "backlite-reader:v1" {
		t.Errorf("AgentImage = %q, want %q", task.AgentImage, "backlite-reader:v1")
	}
	if task.CreatePR {
		t.Error("CreatePR = true, want false for read mode")
	}
	if task.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want pending", task.Status)
	}
	if task.MaxBudgetUSD != 0.5 {
		t.Errorf("MaxBudgetUSD = %v, want 0.5 (read cap)", task.MaxBudgetUSD)
	}
	if task.MaxRuntimeSec != 300 {
		t.Errorf("MaxRuntimeSec = %d, want 300 (read cap)", task.MaxRuntimeSec)
	}
	if task.MaxTurns != 20 {
		t.Errorf("MaxTurns = %d, want 20 (read cap)", task.MaxTurns)
	}
	if len(task.ID) < 4 || task.ID[:3] != "bf_" {
		t.Errorf("ID = %q, want bf_ prefix", task.ID)
	}
}

func TestNewReadTask_EmitsCreatedEvent(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	bus := &capturingEmitter{}
	req := &models.CreateTaskRequest{Prompt: "https://example.com/post"}

	if _, err := NewReadTask(context.Background(), req, s, cfg, bus); err != nil {
		t.Fatalf("NewReadTask: %v", err)
	}
	if len(bus.events) != 1 {
		t.Fatalf("events count = %d, want 1", len(bus.events))
	}
	if bus.events[0].Type != notify.EventTaskCreated {
		t.Errorf("event type = %q, want %q", bus.events[0].Type, notify.EventTaskCreated)
	}
}

func TestNewReadTask_StoreError_WrapsErrStoreFailure(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{createErr: fmt.Errorf("connection refused")}
	req := &models.CreateTaskRequest{Prompt: "https://example.com/post"}

	_, err := NewReadTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrStoreFailure) {
		t.Errorf("error = %v, want errors.Is(err, ErrStoreFailure)", err)
	}
}

func TestNewReadTask_ValidationError_NotStoreFailure(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	req := &models.CreateTaskRequest{Prompt: ""}

	_, err := NewReadTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrStoreFailure) {
		t.Errorf("validation error should not match ErrStoreFailure, got: %v", err)
	}
}

// TestNewReadTask_NoReaderImage_FailsFast ensures a read task is rejected at
// creation time when the server isn't configured with a reader image — no DB
// row gets created and the caller gets an immediate, actionable error.
func TestNewReadTask_NoReaderImage_FailsFast(t *testing.T) {
	cfg := readTestConfig()
	cfg.ReaderImage = ""
	s := &mockStore{}
	req := &models.CreateTaskRequest{Prompt: "https://example.com/post"}

	task, err := NewReadTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected error when ReaderImage is unset, got nil")
	}
	if task != nil {
		t.Errorf("expected nil task on error, got %+v", task)
	}
	if errors.Is(err, ErrStoreFailure) {
		t.Errorf("configuration error should not wrap ErrStoreFailure, got: %v", err)
	}
	if !contains(err.Error(), "BACKFLOW_READER_IMAGE") {
		t.Errorf("error should name the missing env var; got: %v", err)
	}
	if s.createCalls != 0 {
		t.Errorf("store.CreateTask should not be called; got %d calls", s.createCalls)
	}
}

func strPtr(s string) *string { return &s }

// TestNewTask_TaskModeRead_DispatchesToNewReadTask verifies the REST API entry
// point: clients who pass task_mode="read" get a reader-mode task without the
// handler having to know about NewReadTask.
func TestNewTask_TaskModeRead_DispatchesToNewReadTask(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	req := &models.CreateTaskRequest{
		Prompt:   "https://example.com/post",
		TaskMode: strPtr("read"),
	}

	task, err := NewTask(context.Background(), req, s, cfg, nil)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if task.TaskMode != models.TaskModeRead {
		t.Errorf("TaskMode = %q, want %q", task.TaskMode, models.TaskModeRead)
	}
	if task.AgentImage != "backlite-reader:v1" {
		t.Errorf("AgentImage = %q, want reader image", task.AgentImage)
	}
	if task.CreatePR {
		t.Error("CreatePR = true, want false for read mode")
	}
	if task.MaxBudgetUSD != 0.5 {
		t.Errorf("MaxBudgetUSD = %v, want 0.5 (read cap)", task.MaxBudgetUSD)
	}
	if task.MaxRuntimeSec != 300 {
		t.Errorf("MaxRuntimeSec = %d, want 300 (read cap)", task.MaxRuntimeSec)
	}
	if task.MaxTurns != 20 {
		t.Errorf("MaxTurns = %d, want 20 (read cap)", task.MaxTurns)
	}
}

func TestNewTask_ExplicitCodeOrReviewTaskMode_Rejected(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	for _, mode := range []string{"code", "review"} {
		t.Run(mode, func(t *testing.T) {
			req := &models.CreateTaskRequest{
				Prompt:   "Fix bug",
				TaskMode: strPtr(mode),
			}
			_, err := NewTask(context.Background(), req, s, cfg, nil)
			if err == nil {
				t.Fatalf("task_mode=%q: expected validation error, got nil", mode)
			}
			if !contains(err.Error(), "inferred") {
				t.Errorf("task_mode=%q: error = %q, want mention of 'inferred'", mode, err.Error())
			}
		})
	}
}

func TestNewTask_InvalidTaskMode_ValidationError(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	req := &models.CreateTaskRequest{
		Prompt:   "Fix bug",
		TaskMode: strPtr("garbage"),
	}

	_, err := NewTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
	if errors.Is(err, ErrStoreFailure) {
		t.Errorf("validation error should not match ErrStoreFailure, got: %v", err)
	}
	if msg := err.Error(); !contains(msg, "task_mode") {
		t.Errorf("error = %q, want message mentioning task_mode", msg)
	}
}

// contains is a tiny substring helper local to these tests.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestNewTask_CopiesForceFromRequest(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	trueVal := true
	req := &models.CreateTaskRequest{
		Prompt: "Fix bug",
		Force:  &trueVal,
	}

	task, err := NewTask(context.Background(), req, s, cfg, nil)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if !task.Force {
		t.Error("Force = false, want true for non-read task with force=true")
	}
}

func TestNewReadTask_CopiesForceFromRequest(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	trueVal := true
	req := &models.CreateTaskRequest{
		Prompt: "https://example.com/post",
		Force:  &trueVal,
	}

	task, err := NewReadTask(context.Background(), req, s, cfg, nil)
	if err != nil {
		t.Fatalf("NewReadTask: %v", err)
	}
	if !task.Force {
		t.Error("Force = false, want true")
	}
}

// TestNewReadTask_IgnoresPRFields pins the documented contract that read
// tasks never open PRs. A future refactor must not silently start honoring
// PRTitle, PRBody, CreatePR, or SelfReview.
func TestNewReadTask_IgnoresPRFields(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	trueVal := true
	req := &models.CreateTaskRequest{
		Prompt:     "https://example.com/post",
		PRTitle:    "ignored",
		PRBody:     "ignored",
		CreatePR:   &trueVal,
		SelfReview: &trueVal,
	}

	task, err := NewReadTask(context.Background(), req, s, cfg, nil)
	if err != nil {
		t.Fatalf("NewReadTask: %v", err)
	}
	if task.PRTitle != "" {
		t.Errorf("PRTitle = %q, want empty (ignored for read mode)", task.PRTitle)
	}
	if task.PRBody != "" {
		t.Errorf("PRBody = %q, want empty (ignored for read mode)", task.PRBody)
	}
	if task.CreatePR {
		t.Error("CreatePR = true, want false (ignored for read mode)")
	}
	if task.SelfReview {
		t.Error("SelfReview = true, want false (ignored for read mode)")
	}
}

func TestNewTask_RejectsInlineContentOnAutoMode(t *testing.T) {
	cfg := &config.Config{
		AgentImage: "backlite-agent",
	}
	s := &mockStore{}
	body := "# title\nbody\n"
	req := &models.CreateTaskRequest{
		Prompt:        "Fix the bug",
		InlineContent: &body,
	}

	_, err := NewTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected error rejecting inline_content on auto mode; got nil")
	}
	if errors.Is(err, ErrStoreFailure) {
		t.Errorf("error should be a validation error, not ErrStoreFailure: %v", err)
	}
	if s.createCalls != 0 {
		t.Errorf("CreateTask should not be called on validation failure; got %d calls", s.createCalls)
	}
}

func TestNewReadTask_RejectsInlineContentWithURLPrompt(t *testing.T) {
	cfg := readTestConfig()
	s := &mockStore{}
	body := "# title\nbody\n"
	mode := models.TaskModeRead
	req := &models.CreateTaskRequest{
		Prompt:        "https://example.com/post",
		TaskMode:      &mode,
		InlineContent: &body,
	}

	_, err := NewReadTask(context.Background(), req, s, cfg, nil)
	if err == nil {
		t.Fatal("expected error rejecting URL prompt with inline_content; got nil")
	}
	if errors.Is(err, ErrStoreFailure) {
		t.Errorf("error should be a validation error, not ErrStoreFailure: %v", err)
	}
	if s.createCalls != 0 {
		t.Errorf("CreateTask should not be called; got %d calls", s.createCalls)
	}
}

func TestNewReadTask_AllowsInlineContentWithNonURLPrompt(t *testing.T) {
	cfg := readTestConfig()
	cfg.DataDir = t.TempDir()
	s := &mockStore{}
	body := "# title\nbody\n"
	mode := models.TaskModeRead
	req := &models.CreateTaskRequest{
		Prompt:        "anything",
		TaskMode:      &mode,
		InlineContent: &body,
	}

	// The URL guard must not fire for a non-URL prompt; the call should
	// reach persistence and succeed against the in-memory mock store.
	if _, err := NewReadTask(context.Background(), req, s, cfg, nil); err != nil {
		t.Errorf("NewReadTask with non-URL prompt should succeed, got: %v", err)
	}
}

func TestNewReadTask_WithInlineContent_WritesFileAndRewritesPrompt(t *testing.T) {
	// Arrange: a config pointing DataDir at a tempdir, and a captured store
	// that records what task is persisted.
	dataDir := t.TempDir()
	cfg := readTestConfig()
	cfg.DataDir = dataDir
	bus := &capturingEmitter{}

	captured := &capturingStore{}
	body := "# Title\n\nbody text\n"
	mode := models.TaskModeRead
	req := &models.CreateTaskRequest{
		Prompt:        "ingest a local note",
		TaskMode:      &mode,
		InlineContent: &body,
	}

	// Act
	task, err := NewReadTask(context.Background(), req, captured, cfg, bus)
	if err != nil {
		t.Fatalf("NewReadTask: %v", err)
	}

	// Assert: SHA matches the body, prompt rewritten, on-disk file present.
	wantSHA := sha256Hex([]byte(body))
	if task.InlineContentSHA256 != wantSHA {
		t.Errorf("InlineContentSHA256 = %q, want %q", task.InlineContentSHA256, wantSHA)
	}
	wantPrompt := "markdown://" + wantSHA
	if task.Prompt != wantPrompt {
		t.Errorf("Prompt = %q, want %q", task.Prompt, wantPrompt)
	}

	wantPath := filepath.Join(dataDir, "ingest", wantSHA+".md")
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read persisted ingest file: %v", err)
	}
	if string(got) != body {
		t.Errorf("on-disk body = %q, want %q", got, body)
	}

	// Store received the rewritten task.
	if captured.lastTask == nil {
		t.Fatal("CreateTask was not called")
	}
	if captured.lastTask.Prompt != wantPrompt {
		t.Errorf("store received Prompt = %q, want %q", captured.lastTask.Prompt, wantPrompt)
	}
	if captured.lastTask.InlineContentSHA256 != wantSHA {
		t.Errorf("store received InlineContentSHA256 = %q, want %q", captured.lastTask.InlineContentSHA256, wantSHA)
	}

	// task.created event was emitted.
	if len(bus.events) != 1 || bus.events[0].Type != notify.EventTaskCreated {
		t.Errorf("expected one task.created event, got %+v", bus.events)
	}
}

// capturingStore records the task last passed to CreateTask.
type capturingStore struct {
	mockStore
	lastTask *models.Task
}

func (c *capturingStore) CreateTask(ctx context.Context, t *models.Task) error {
	c.lastTask = t
	return c.mockStore.CreateTask(ctx, t)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
