package lifecycle

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

// captureEmitter records events for test assertions.
type captureEmitter struct {
	mu     sync.Mutex
	events []notify.Event
}

func (c *captureEmitter) Emit(e notify.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureEmitter) Events() []notify.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]notify.Event, len(c.events))
	copy(out, c.events)
	return out
}

// newTestStore bootstraps a real SQLite store against a tempfile database with
// goose migrations applied.
func newTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	ctx := context.Background()
	migrationsDir := filepath.Join("..", "..", "..", "migrations")
	dbPath := filepath.Join(t.TempDir(), "lifecycle-test.db")
	s, err := store.NewSQLite(ctx, dbPath, migrationsDir)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	return s
}

// seedTask inserts a task in the given status.
func seedTask(t *testing.T, s store.Store, id string, status models.TaskStatus) *models.Task {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := &models.Task{
		ID:        id,
		Status:    status,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Branch:    "backlite/test",
		Prompt:    "Fix the bug",
		Model:     "claude-sonnet-4-6",
		CreatePR:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func TestMarkReadyForRetry_SetsFlag(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := seedTask(t, s, "bf_LIFE001", models.TaskStatusFailed)

	emitter := &captureEmitter{}
	c := New(s, emitter)

	if err := c.MarkReadyForRetry(ctx, task.ID); err != nil {
		t.Fatalf("MarkReadyForRetry: %v", err)
	}

	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !got.ReadyForRetry {
		t.Errorf("ReadyForRetry = false, want true")
	}
	if len(emitter.Events()) != 0 {
		t.Errorf("events emitted = %d, want 0 (event emission lives in markRetryReady this phase)", len(emitter.Events()))
	}
}

func TestMarkReadyForRetry_UnknownTaskIsNoop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	emitter := &captureEmitter{}
	c := New(s, emitter)

	// Matches the underlying store.MarkReadyForRetry behavior: a missing row
	// is not an error (the UPDATE simply matches zero rows).
	if err := c.MarkReadyForRetry(ctx, "bf_DOESNOTEXIST0000000000000"); err != nil {
		t.Fatalf("MarkReadyForRetry on missing task: %v", err)
	}
	if len(emitter.Events()) != 0 {
		t.Errorf("events emitted = %d, want 0", len(emitter.Events()))
	}
}
