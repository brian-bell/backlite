package api

import (
	"context"
	"errors"
	"fmt"
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

func taskTestConfig() *config.Config {
	return &config.Config{
		AgentImage:         "backlite-agent",
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultCodexModel:  "gpt-5.4",
		DefaultEffort:      "medium",
		DefaultMaxBudget:   10,
		DefaultMaxRuntime:  1800_000_000_000, // 1800s in ns
		DefaultMaxTurns:    200,
		DefaultSaveOutput:  true,
	}
}

func TestNewTask_EmitsCreatedEvent(t *testing.T) {
	cfg := taskTestConfig()
	s := &mockStore{}
	bus := &capturingEmitter{}
	req := &models.CreateTaskRequest{Prompt: "Fix bug"}

	if _, err := NewTask(context.Background(), req, s, cfg, bus); err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if len(bus.events) != 1 {
		t.Fatalf("events count = %d, want 1", len(bus.events))
	}
	if bus.events[0].Type != notify.EventTaskCreated {
		t.Errorf("event type = %q, want %q", bus.events[0].Type, notify.EventTaskCreated)
	}
}

func TestNewTask_SetsDefaults(t *testing.T) {
	cfg := taskTestConfig()
	s := &mockStore{}
	req := &models.CreateTaskRequest{Prompt: "Fix bug"}

	task, err := NewTask(context.Background(), req, s, cfg, nil)
	if err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if task.TaskMode != models.TaskModeAuto {
		t.Errorf("TaskMode = %q, want %q", task.TaskMode, models.TaskModeAuto)
	}
	if task.AgentImage != "backlite-agent" {
		t.Errorf("AgentImage = %q, want %q", task.AgentImage, "backlite-agent")
	}
	if task.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want pending", task.Status)
	}
	if len(task.ID) < 4 || task.ID[:3] != "bf_" {
		t.Errorf("ID = %q, want bf_ prefix", task.ID)
	}
}

func strPtr(s string) *string { return &s }

func TestNewTask_ExplicitCodeOrReviewTaskMode_Rejected(t *testing.T) {
	cfg := taskTestConfig()
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
	cfg := taskTestConfig()
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
