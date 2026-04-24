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

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

// Emitter is the narrow notify interface the coordinator needs. *notify.EventBus
// satisfies this without the coordinator depending on the bus's full API.
type Emitter interface {
	Emit(notify.Event)
}

// Result is the terminal outcome of a running task, passed to Complete.
type Result struct {
	Status         models.TaskStatus
	Error          string
	PRURL          string
	OutputURL      string
	LogTail        string
	CostUSD        float64
	ElapsedTimeSec int
	RepoURL        string
	TargetBranch   string
	TaskMode       string
	// ReadingOpt, when non-nil, is applied to the emitted task.completed event
	// to populate reading-specific fields (populated by the reading pipeline).
	ReadingOpt notify.EventOption
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
	store   store.Store
	emitter Emitter
}

// New constructs a Coordinator.
func New(s store.Store, emitter Emitter) *Coordinator {
	return &Coordinator{store: s, emitter: emitter}
}

// MarkReadyForRetry flips the ready_for_retry gate on a task so subsequent
// monitor ticks won't reprocess it. The paired event emission still lives in
// the orchestrator's markRetryReady helper this phase; later phases absorb it.
func (c *Coordinator) MarkReadyForRetry(ctx context.Context, taskID string) error {
	return c.store.MarkReadyForRetry(ctx, taskID)
}
