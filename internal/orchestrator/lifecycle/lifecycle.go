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

// Emitter is re-exported from notify for convenience — the coordinator only
// needs the narrow Emit method, which *notify.EventBus already satisfies.
type Emitter = notify.Emitter

// Slots exposes the orchestrator's in-memory running-task counter so the
// coordinator can pair its DB writes with the local accounting update.
//   - Acquire is called after a successful Start transition.
//   - Release decrements the local running counter.
//
// The orchestrator's incrementRunning + releaseSlot methods satisfy this.
type Slots interface {
	Acquire()
	Release(ctx context.Context, task *models.Task)
}

// noopSlots is the zero-value Slots used when the coordinator is constructed
// without a real counter — convenient for tests that only exercise DB writes
// and don't care about slot accounting.
type noopSlots struct{}

func (noopSlots) Acquire()                              {}
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
	// ChainTx, if non-nil, runs inside the same SQLite transaction that
	// writes the parent's terminal state. Returning a non-nil child task
	// causes Complete to emit task.created for it after the tx commits.
	// Used by the chain module for atomic self-review chained-task creation.
	ChainTx func(ctx context.Context, tx store.Store) (*models.Task, error)
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
// can pair its DB writes with the local running counter.
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

// Assign transitions a task pending → provisioning. Used as the first step of
// dispatch before the caller triggers the external RunAgent side-effect.
func (c *Coordinator) Assign(ctx context.Context, taskID string) error {
	if err := c.store.AssignTask(ctx, taskID); err != nil {
		return fmt.Errorf("assign task: %w", err)
	}
	return nil
}

// Start transitions a task provisioning → running, bumps the local running
// counter, and emits task.running. The caller is responsible for invoking
// docker.RunAgent between Assign and Start. task.AgentImage is persisted at
// this point so the DB records the image actually used (after any image-router
// override), not just the creation-time default.
func (c *Coordinator) Start(ctx context.Context, task *models.Task, containerID string) error {
	if err := c.store.StartTask(ctx, task.ID, containerID, task.AgentImage); err != nil {
		return fmt.Errorf("start task: %w", err)
	}
	c.slots.Acquire()
	c.emitter.Emit(notify.NewEvent(notify.EventTaskRunning, task))
	return nil
}

// Cancel transitions a task to cancelled from any non-terminal state and
// emits task.cancelled. Rejects tasks already in a terminal state
// (completed, failed, cancelled, interrupted). Container cleanup + slot
// release happen later via monitorCancelled — Cancel itself is a pure
// state-write + event-emit.
func (c *Coordinator) Cancel(ctx context.Context, task *models.Task) error {
	switch task.Status {
	case models.TaskStatusPending, models.TaskStatusProvisioning, models.TaskStatusRunning, models.TaskStatusRecovering:
	default:
		return fmt.Errorf("task %s cannot be cancelled (status: %s)", task.ID, task.Status)
	}
	if err := c.store.CancelTask(ctx, task.ID); err != nil {
		return fmt.Errorf("cancel task: %w", err)
	}
	c.emitter.Emit(notify.NewEvent(notify.EventTaskCancelled, task))
	return nil
}

// FailDispatch marks a task as failed during the dispatch phase (before it
// reaches running) and emits task.failed. Unlike Complete, no slot release
// happens and ready_for_retry is not flipped — dispatch failures are
// systematic (missing config, duplicate read, no capacity) and callers
// should resubmit rather than retry.
func (c *Coordinator) FailDispatch(ctx context.Context, task *models.Task, reason string) error {
	if err := c.store.UpdateTaskStatus(ctx, task.ID, models.TaskStatusFailed, reason); err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("lifecycle.FailDispatch: update status failed")
	}
	c.emitter.Emit(notify.NewEvent(notify.EventTaskFailed, task, notify.WithContainerStatus("", reason, "")))
	return nil
}

// MarkRecovering transitions a task to recovering, optionally clears its
// container assignment, and emits task.recovering. Performs every
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

	var (
		writeErr      error
		chainChild    *models.Task
		parentWritten bool
	)
	if r.ChainTx != nil {
		// Atomic path: parent COMPLETE + chain hook (e.g. child INSERT) run in
		// the same SQLite tx. If either fails, the whole pair rolls back.
		err := c.store.WithTx(ctx, func(tx store.Store) error {
			if err := tx.CompleteTask(ctx, task.ID, storeResult); err != nil {
				return fmt.Errorf("complete parent: %w", err)
			}
			child, err := r.ChainTx(ctx, tx)
			if err != nil {
				return fmt.Errorf("chain tx: %w", err)
			}
			chainChild = child
			return nil
		})
		if err == nil {
			c.refreshTask(ctx, task, storeResult, now)
			parentWritten = true
		} else {
			// Chain tx rolled back. Fall through to the non-chain path so the
			// parent still commits — losing the chained review is preferable
			// to leaving the parent stuck in `running` and re-firing this code
			// path on every monitor tick.
			log.Warn().Err(err).Str("task_id", task.ID).Msg("lifecycle.Complete: chain tx failed; falling back to non-chain completion")
			chainChild = nil
		}
	}
	if !parentWritten {
		if err := c.store.CompleteTask(ctx, task.ID, storeResult); err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("lifecycle.Complete: failed to complete task in store")
			writeErr = err
		} else {
			c.refreshTask(ctx, task, storeResult, now)
		}
	}

	if writeErr != nil {
		// We never persisted the terminal state. Don't release the slot,
		// don't emit, don't flip ready_for_retry — those would lie about
		// state we couldn't write, and (because the DB row is still
		// running) the next monitor tick will reprocess the exited
		// container and call Complete again. Releasing here would also
		// drop the slot accounting below the live container count.
		return writeErr
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
	if chainChild != nil {
		c.emitter.Emit(notify.NewEvent(notify.EventTaskCreated, chainChild))
	}
	return nil
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
// task.running is emitted. Otherwise the task is returned to pending via
// Requeue(RequeueRecovering). Any orphan that held a container releases its
// local slot before the requeue write.
func (c *Coordinator) Recover(ctx context.Context, task *models.Task, containerAlive bool, reason string) error {
	if containerAlive {
		if err := c.store.UpdateTaskStatus(ctx, task.ID, models.TaskStatusRunning, ""); err != nil {
			return fmt.Errorf("promote to running: %w", err)
		}
		c.emitter.Emit(notify.NewEvent(notify.EventTaskRunning, task, notify.WithContainerStatus("", "recovered: container still running", "")))
		return nil
	}
	return c.Requeue(ctx, task, reason, RequeueRecovering)
}

// Requeue returns a task to pending for another attempt. It clears output_url
// (already part of store.RequeueTask), releases the local slot if the task
// held a container, and emits task.interrupted (RequeueInterrupted) or
// task.recovering (RequeueRecovering). This is the sole requeue path now
// that both the monitor and recovery loops route through the coordinator.
func (c *Coordinator) Requeue(ctx context.Context, task *models.Task, reason string, kind RequeueKind) error {
	if task.ContainerID != "" {
		c.slots.Release(ctx, task)
	}
	if err := c.store.RequeueTask(ctx, task.ID, reason); err != nil {
		return fmt.Errorf("requeue: %w", err)
	}

	eventType := notify.EventTaskInterrupted
	if kind == RequeueRecovering {
		eventType = notify.EventTaskRecovering
	}
	c.emitter.Emit(notify.NewEvent(eventType, task, notify.WithContainerStatus("", reason, "")))
	return nil
}
