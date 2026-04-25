package chain_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/orchestrator/chain"
	"github.com/brian-bell/backlite/internal/orchestrator/lifecycle"
	"github.com/brian-bell/backlite/internal/store"
)

// captureEmitter records events for assertions.
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

func newTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	migrationsDir := filepath.Join("..", "..", "..", "migrations")
	dbPath := filepath.Join(t.TempDir(), "chain-test.db")
	s, err := store.NewSQLite(context.Background(), dbPath, migrationsDir)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedRunningParent(t *testing.T, s store.Store) *models.Task {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := &models.Task{
		ID:         "bf_PARENT_LIVE",
		Status:     models.TaskStatusRunning,
		TaskMode:   models.TaskModeCode,
		Harness:    models.HarnessClaudeCode,
		RepoURL:    "https://github.com/test/repo",
		Prompt:     "Fix the auth bug",
		SelfReview: true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// TestLifecycle_Complete_WithChain_AtomicSuccess verifies the happy path: a
// successful self-review code task triggers atomic parent COMPLETE + child
// INSERT, and downstream callers see two webhook events (task.completed for
// parent, task.created for child). The child carries parent_task_id and the
// flat $2 budget.
func TestLifecycle_Complete_WithChain_AtomicSuccess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	parent := seedRunningParent(t, s)

	emitter := &captureEmitter{}
	c := lifecycle.New(s, emitter)

	planned, ok := chain.Plan(&models.Task{
		ID:         parent.ID,
		Status:     models.TaskStatusCompleted,
		TaskMode:   parent.TaskMode,
		Harness:    parent.Harness,
		SelfReview: parent.SelfReview,
		PRURL:      "https://github.com/test/repo/pull/100",
		Prompt:     parent.Prompt,
	})
	if !ok || planned == nil {
		t.Fatalf("chain.Plan returned (nil, false), want eligible child")
	}

	err := c.Complete(ctx, parent, lifecycle.Result{
		Status:    models.TaskStatusCompleted,
		EventType: notify.EventTaskCompleted,
		PRURL:     "https://github.com/test/repo/pull/100",
		EventOpts: []notify.EventOption{notify.WithContainerStatus("https://github.com/test/repo/pull/100", "", "")},
		ChainTx: func(txCtx context.Context, tx store.Store) (*models.Task, error) {
			return planned, chain.CreateChild(txCtx, tx, planned)
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Parent persisted as completed with PR URL.
	gotParent, err := s.GetTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetTask parent: %v", err)
	}
	if gotParent.Status != models.TaskStatusCompleted {
		t.Errorf("parent.Status = %q, want completed", gotParent.Status)
	}
	if gotParent.PRURL != "https://github.com/test/repo/pull/100" {
		t.Errorf("parent.PRURL = %q", gotParent.PRURL)
	}

	// Child persisted with parent_task_id + flat $2 budget.
	gotChild, err := s.GetTask(ctx, planned.ID)
	if err != nil {
		t.Fatalf("GetTask child: %v", err)
	}
	if gotChild.ParentTaskID == nil || *gotChild.ParentTaskID != parent.ID {
		t.Errorf("child.ParentTaskID = %v, want %q", gotChild.ParentTaskID, parent.ID)
	}
	if gotChild.MaxBudgetUSD != chain.SelfReviewBudgetUSD {
		t.Errorf("child.MaxBudgetUSD = %v, want %v", gotChild.MaxBudgetUSD, chain.SelfReviewBudgetUSD)
	}
	if gotChild.TaskMode != models.TaskModeReview {
		t.Errorf("child.TaskMode = %q, want review", gotChild.TaskMode)
	}
	if gotChild.Status != models.TaskStatusPending {
		t.Errorf("child.Status = %q, want pending", gotChild.Status)
	}

	// Two events: parent.completed, then child.created. Child event includes
	// parent_task_id pointer.
	evs := emitter.Events()
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2 (parent.completed + child.created); got %+v", len(evs), evs)
	}
	if evs[0].Type != notify.EventTaskCompleted || evs[0].TaskID != parent.ID {
		t.Errorf("event 0 = %+v, want task.completed for parent", evs[0])
	}
	if evs[1].Type != notify.EventTaskCreated || evs[1].TaskID != planned.ID {
		t.Errorf("event 1 = %+v, want task.created for child", evs[1])
	}
	if evs[1].ParentTaskID == nil || *evs[1].ParentTaskID != parent.ID {
		t.Errorf("child event ParentTaskID = %v, want %q", evs[1].ParentTaskID, parent.ID)
	}
}

// TestLifecycle_Complete_WithChain_TxRollback verifies atomicity: if the
// child insert fails inside the chain tx, the parent's COMPLETE rolls back
// and the parent stays in its pre-call status.
func TestLifecycle_Complete_WithChain_TxRollback(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	parent := seedRunningParent(t, s)

	emitter := &captureEmitter{}
	c := lifecycle.New(s, emitter)

	injected := errors.New("injected child insert failure")

	err := c.Complete(ctx, parent, lifecycle.Result{
		Status:    models.TaskStatusCompleted,
		EventType: notify.EventTaskCompleted,
		PRURL:     "https://github.com/test/repo/pull/200",
		ChainTx: func(_ context.Context, _ store.Store) (*models.Task, error) {
			return nil, injected
		},
	})
	if err == nil {
		t.Fatalf("Complete: expected error from injected chain failure, got nil")
	}
	if !errors.Is(err, injected) {
		t.Errorf("error chain = %v, want wraps %v", err, injected)
	}

	gotParent, err := s.GetTask(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetTask parent: %v", err)
	}
	// Parent must still be running (rolled back), not completed.
	if gotParent.Status != models.TaskStatusRunning {
		t.Errorf("parent.Status after rollback = %q, want running (tx rolled back)", gotParent.Status)
	}
	if gotParent.PRURL != "" {
		t.Errorf("parent.PRURL after rollback = %q, want empty (tx rolled back)", gotParent.PRURL)
	}

	// Even on rollback, lifecycle still emits the parent's completion event so
	// downstream consumers see the outcome. The child.created event must NOT
	// fire because no child was persisted.
	evs := emitter.Events()
	hasCreated := false
	for _, e := range evs {
		if e.Type == notify.EventTaskCreated {
			hasCreated = true
		}
	}
	if hasCreated {
		t.Errorf("task.created event emitted despite rollback; events = %+v", evs)
	}
}

// TestLifecycle_Complete_NoChainTx_BackwardCompatible verifies the existing
// non-chain Complete path still works and emits exactly one event.
func TestLifecycle_Complete_NoChainTx_BackwardCompatible(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	parent := seedRunningParent(t, s)

	emitter := &captureEmitter{}
	c := lifecycle.New(s, emitter)

	err := c.Complete(ctx, parent, lifecycle.Result{
		Status:    models.TaskStatusCompleted,
		EventType: notify.EventTaskCompleted,
		PRURL:     "https://github.com/test/repo/pull/300",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	gotParent, _ := s.GetTask(ctx, parent.ID)
	if gotParent.Status != models.TaskStatusCompleted {
		t.Errorf("parent.Status = %q, want completed", gotParent.Status)
	}

	evs := emitter.Events()
	if len(evs) != 1 || evs[0].Type != notify.EventTaskCompleted {
		t.Errorf("events = %+v, want one task.completed", evs)
	}
}
