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

// trackingSlots records every Release call so tests can assert the coordinator
// pairs DB writes with the local counter + instance-slot decrement.
type trackingSlots struct {
	mu       sync.Mutex
	released []string // task IDs in release order
}

func (s *trackingSlots) Release(_ context.Context, task *models.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, task.ID)
}

func (s *trackingSlots) Released() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.released))
	copy(out, s.released)
	return out
}

func seedInstance(t *testing.T, s store.Store, id string, running int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	inst := &models.Instance{
		InstanceID:        id,
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: running,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
}

func TestMarkRecovering_ProvisioningOrphan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := seedTask(t, s, "bf_MR_PROV", models.TaskStatusProvisioning)
	// Mirror recoverOnStartup: provisioning orphan has instance assigned.
	task.InstanceID = "local"
	_ = s.AssignTask(ctx, task.ID, "local")

	emitter := &captureEmitter{}
	c := New(s, emitter, WithSlots(&trackingSlots{}))

	c.MarkRecovering(ctx, task, true, "recovering after server restart (was provisioning)")

	got, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != models.TaskStatusRecovering {
		t.Errorf("status = %q, want recovering", got.Status)
	}
	if got.InstanceID != "" {
		t.Errorf("instance_id = %q, want empty (clearAssignment=true)", got.InstanceID)
	}
	evs := emitter.Events()
	if len(evs) != 1 || evs[0].Type != notify.EventTaskRecovering {
		t.Fatalf("events = %+v, want one task.recovering", evs)
	}
	if evs[0].Message != "recovering after server restart (was provisioning)" {
		t.Errorf("message = %q", evs[0].Message)
	}
}

func TestMarkRecovering_RunningOrphan_PreservesAssignment(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := seedTask(t, s, "bf_MR_RUN", models.TaskStatusRunning)
	_ = s.AssignTask(ctx, task.ID, "local")
	_ = s.StartTask(ctx, task.ID, "cont_123")
	task, _ = s.GetTask(ctx, task.ID)

	emitter := &captureEmitter{}
	c := New(s, emitter, WithSlots(&trackingSlots{}))

	c.MarkRecovering(ctx, task, false, "recovering after server restart (was running)")

	got, _ := s.GetTask(ctx, task.ID)
	if got.Status != models.TaskStatusRecovering {
		t.Errorf("status = %q, want recovering", got.Status)
	}
	if got.InstanceID != "local" || got.ContainerID != "cont_123" {
		t.Errorf("assignment wiped despite clearAssignment=false: instance=%q container=%q", got.InstanceID, got.ContainerID)
	}
	if len(emitter.Events()) != 1 {
		t.Fatalf("events = %d, want 1", len(emitter.Events()))
	}
}

func TestRecover_ContainerAlive_PromotesToRunning(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := seedTask(t, s, "bf_REC_ALIVE", models.TaskStatusRecovering)

	emitter := &captureEmitter{}
	slots := &trackingSlots{}
	c := New(s, emitter, WithSlots(slots))

	if err := c.Recover(ctx, task, true, "inspect ok"); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, _ := s.GetTask(ctx, task.ID)
	if got.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	evs := emitter.Events()
	if len(evs) != 1 || evs[0].Type != notify.EventTaskRunning {
		t.Fatalf("events = %+v, want one task.running", evs)
	}
	if got := slots.Released(); len(got) != 0 {
		t.Errorf("slots released on promote = %v, want none", got)
	}
}

func TestRecover_NoContainer_RequeuesWithoutSlotRelease(t *testing.T) {
	// Provisioning-orphan path: no container was ever started, so neither
	// the local counter nor the instance slot should be released.
	s := newTestStore(t)
	ctx := context.Background()
	seedInstance(t, s, "local", 0)
	task := seedTask(t, s, "bf_REC_NOCONT", models.TaskStatusRecovering)
	// task.ContainerID stays empty (was provisioning).

	emitter := &captureEmitter{}
	slots := &trackingSlots{}
	c := New(s, emitter, WithSlots(slots))

	if err := c.Recover(ctx, task, false, "no container (was provisioning)"); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, _ := s.GetTask(ctx, task.ID)
	if got.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got := slots.Released(); len(got) != 0 {
		t.Errorf("slots released on no-container path = %v, want none", got)
	}
	inst, _ := s.GetInstance(ctx, "local")
	if inst.RunningContainers != 0 {
		t.Errorf("instance RunningContainers = %d, want 0", inst.RunningContainers)
	}
}

func TestRecover_WasRunning_ReleasesBothCounters(t *testing.T) {
	// Was-running orphan path: the instance was incremented at startup
	// fix-up, and the local counter is also nonzero. Both must be released.
	// This covers the pre-refactor drift bug at recovery.go:150 where the
	// local counter decremented but the instance slot did not.
	s := newTestStore(t)
	ctx := context.Background()
	seedInstance(t, s, "local", 1)
	task := seedTask(t, s, "bf_REC_WASRUN", models.TaskStatusRecovering)
	_ = s.AssignTask(ctx, task.ID, "local")
	_ = s.StartTask(ctx, task.ID, "cont_wasrun")
	task, _ = s.GetTask(ctx, task.ID)

	emitter := &captureEmitter{}
	slots := &trackingSlots{}
	c := New(s, emitter, WithSlots(slots))

	if err := c.Recover(ctx, task, false, "instance gone"); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, _ := s.GetTask(ctx, task.ID)
	if got.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.InstanceID != "" || got.ContainerID != "" {
		t.Errorf("assignment not cleared by requeue: instance=%q container=%q", got.InstanceID, got.ContainerID)
	}
	if got := slots.Released(); len(got) != 1 || got[0] != task.ID {
		t.Errorf("slots.Release calls = %v, want [%q]", got, task.ID)
	}
}
