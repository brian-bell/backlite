// Package lifecycle owns task state transitions, slot accounting, and paired
// event emission for the orchestrator. Callers request a domain-level intent
// (Dispatch, Complete, Requeue, Cancel, MarkReadyForRetry, Recover) and the
// Coordinator picks the right Store method, releases slots, and emits the
// matching notify.Event.
//
// The Coordinator grows method by method alongside the issue #32 migration;
// new methods land in their own PR with the callers switched over.
package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

// Emitter is the narrow notify interface the coordinator needs. *notify.EventBus
// satisfies this without the coordinator depending on the bus's full API.
type Emitter interface {
	Emit(notify.Event)
}

// Slots releases the orchestrator's in-memory running-task counter together
// with the instance's DB-backed running_containers slot. The orchestrator's
// releaseSlot method satisfies this implicitly.
type Slots interface {
	Release(ctx context.Context, task *models.Task)
}

// noopSlots is the zero-value Slots used when the coordinator is constructed
// without a real counter — convenient for tests that only exercise DB writes
// and don't care about slot accounting.
type noopSlots struct{}

func (noopSlots) Release(context.Context, *models.Task) {}

// Result is the terminal outcome of a running task, passed to Complete. The
// caller supplies both the persisted fields (Status, Error, PRURL, …) and the
// event-shape fields (EventType + EventOpts) — Complete writes the DB row,
// reloads it, releases slots, optionally flips ready_for_retry (for
// non-success terminals), and emits exactly one event.
type Result struct {
	Status         models.TaskStatus
	EventType      notify.EventType
	Error          string
	PRURL          string
	OutputURL      string
	CostUSD        float64
	ElapsedTimeSec int
	RepoURL        string
	TargetBranch   string
	TaskMode       string
	// EventOpts are applied to the emitted event (e.g. WithContainerStatus,
	// WithReading). The retry-gate option (WithReadyForRetry or
	// WithRetryLimitReached) is applied automatically for non-success terminals.
	EventOpts []notify.EventOption
}

// RequeueKind selects the event emitted when a task is returned to pending.
type RequeueKind int

const (
	// RequeueInterrupted emits task.interrupted — used when the orchestrator
	// loses a task mid-run and returns it to the queue.
	RequeueInterrupted RequeueKind = iota
	// RequeueRecovering emits task.recovering — used for recovery-path requeues
	// at startup or after the orphan has been inspected.
	RequeueRecovering
)

// Coordinator owns task state transitions, slot accounting, and paired event
// emission. See the package docstring for the full method set; methods land
// in the Coordinator phase by phase and callers migrate file by file.
type Coordinator struct {
	store          store.Store
	emitter        Emitter
	slots          Slots
	maxUserRetries int
}

// Option customises a Coordinator at construction.
type Option func(*Coordinator)

// WithSlots wires in the orchestrator's slot-release hook so the coordinator
// can pair its DB writes with the local counter + instance-slot decrement.
// Omit in tests that don't care about slot accounting.
func WithSlots(s Slots) Option {
	return func(c *Coordinator) { c.slots = s }
}

// WithMaxUserRetries sets the user-retry cap used by Complete to decide
// whether to emit WithReadyForRetry or WithRetryLimitReached on non-success
// terminals. Defaults to zero (every failure emits RetryLimitReached).
func WithMaxUserRetries(n int) Option {
	return func(c *Coordinator) { c.maxUserRetries = n }
}

// New constructs a Coordinator.
func New(s store.Store, emitter Emitter, opts ...Option) *Coordinator {
	c := &Coordinator{store: s, emitter: emitter, slots: noopSlots{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// MarkReadyForRetry flips the ready_for_retry gate on a task so subsequent
// monitor ticks won't reprocess it. The paired event emission still lives in
// the orchestrator's markRetryReady helper this phase; later phases absorb it.
func (c *Coordinator) MarkReadyForRetry(ctx context.Context, taskID string) error {
	return c.store.MarkReadyForRetry(ctx, taskID)
}

// MarkRecovering transitions a task to recovering, optionally clears its
// instance/container assignment, and emits task.recovering. Performs every
// step best-effort: internal errors are logged but do not short-circuit the
// event emission, preserving the pre-refactor invariant that orphan-recovery
// notifications fire even if a momentary DB hiccup skips the status write.
func (c *Coordinator) MarkRecovering(ctx context.Context, task *models.Task, clearAssignment bool, message string) {
	if err := c.store.UpdateTaskStatus(ctx, task.ID, models.TaskStatusRecovering, ""); err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("lifecycle.MarkRecovering: update status failed")
	}
	if clearAssignment {
		if err := c.store.ClearTaskAssignment(ctx, task.ID); err != nil {
			log.Warn().Err(err).Str("task_id", task.ID).Msg("lifecycle.MarkRecovering: clear assignment failed")
		}
	}
	c.emitter.Emit(notify.NewEvent(notify.EventTaskRecovering, task, notify.WithContainerStatus("", message, "")))
}

// Complete finishes a running task with a terminal result: writes CompleteTask,
// reloads the row into the supplied task pointer, releases slots, flips the
// ready_for_retry gate for non-success terminals, and emits exactly one
// event carrying the caller's EventType + EventOpts (plus the retry-gate
// option, if applicable). A DB failure on the CompleteTask write is logged
// but still runs the slot release and event emission — matching the
// pre-refactor behavior where users still get a notification even when a
// momentary DB hiccup skips the status write.
func (c *Coordinator) Complete(ctx context.Context, task *models.Task, r Result) error {
	now := time.Now().UTC()
	storeResult := store.TaskResult{
		Status:         r.Status,
		Error:          r.Error,
		PRURL:          r.PRURL,
		OutputURL:      r.OutputURL,
		CostUSD:        r.CostUSD,
		ElapsedTimeSec: r.ElapsedTimeSec,
		RepoURL:        r.RepoURL,
		TargetBranch:   r.TargetBranch,
		TaskMode:       r.TaskMode,
	}

	var writeErr error
	if err := c.store.CompleteTask(ctx, task.ID, storeResult); err != nil {
		log.Error().Err(err).Str("task_id", task.ID).Msg("lifecycle.Complete: failed to complete task in store")
		applyTaskResult(task, storeResult, now)
		writeErr = err
	} else {
		c.refreshTask(ctx, task, storeResult, now)
	}

	c.slots.Release(ctx, task)

	opts := append([]notify.EventOption{}, r.EventOpts...)
	if r.Status != models.TaskStatusCompleted {
		if err := c.store.MarkReadyForRetry(ctx, task.ID); err != nil {
			log.Warn().Err(err).Str("task_id", task.ID).Msg("lifecycle.Complete: failed to mark ready for retry")
		}
		if task.UserRetryCount < c.maxUserRetries {
			opts = append([]notify.EventOption{notify.WithReadyForRetry()}, opts...)
		} else {
			opts = append([]notify.EventOption{notify.WithRetryLimitReached()}, opts...)
		}
	}
	c.emitter.Emit(notify.NewEvent(r.EventType, task, opts...))
	return writeErr
}

// refreshTask reloads the task row from the store so the caller's pointer
// reflects persisted fields. Falls back to patching the pointer from the
// result on reload failure.
func (c *Coordinator) refreshTask(ctx context.Context, task *models.Task, r store.TaskResult, completedAt time.Time) {
	fresh, err := c.store.GetTask(ctx, task.ID)
	if err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("lifecycle.Complete: failed to reload completed task")
		applyTaskResult(task, r, completedAt)
		return
	}
	*task = *fresh
}

// applyTaskResult patches an in-memory task pointer with a completion result
// when the DB reload fails. Keeps the event payload and any downstream
// consumers consistent with the intended terminal state.
func applyTaskResult(task *models.Task, r store.TaskResult, completedAt time.Time) {
	task.Status = r.Status
	task.Error = r.Error
	task.PRURL = r.PRURL
	task.OutputURL = r.OutputURL
	task.CostUSD = r.CostUSD
	task.ElapsedTimeSec = r.ElapsedTimeSec
	if r.RepoURL != "" {
		task.RepoURL = r.RepoURL
	}
	if r.TargetBranch != "" {
		task.TargetBranch = r.TargetBranch
	}
	if r.TaskMode != "" {
		task.TaskMode = r.TaskMode
	}
	task.CompletedAt = &completedAt
}

// Recover handles a recovering-state task after startup has classified it.
// If containerAlive is true, the task is promoted back to running and
// task.running is emitted. Otherwise the task is returned to pending; any
// orphan that held a container (ContainerID != "") gets both counters
// released before the requeue write, which also fixes the pre-refactor drift
// where recovery-requeue only released the local counter.
func (c *Coordinator) Recover(ctx context.Context, task *models.Task, containerAlive bool, reason string) error {
	if containerAlive {
		if err := c.store.UpdateTaskStatus(ctx, task.ID, models.TaskStatusRunning, ""); err != nil {
			return fmt.Errorf("promote to running: %w", err)
		}
		c.emitter.Emit(notify.NewEvent(notify.EventTaskRunning, task, notify.WithContainerStatus("", "recovered: container still running", "")))
		return nil
	}

	if task.ContainerID != "" {
		c.slots.Release(ctx, task)
	}
	if err := c.store.RequeueTask(ctx, task.ID, reason); err != nil {
		return fmt.Errorf("requeue: %w", err)
	}
	return nil
}
