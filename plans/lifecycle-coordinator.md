# Plan: TaskLifecycle coordinator

> Source PRD: https://github.com/brian-bell/backlite/issues/32

Extract task state-transition, slot accounting, and paired event emission into a dedicated `internal/orchestrator/lifecycle/` package. The refactor keeps the existing `Store` interface intact during migration and moves callers file-by-file (recovery → monitor → dispatch → api).

## Architectural decisions

Durable decisions that apply across all phases:

- **Package path**: `internal/orchestrator/lifecycle/` — new subpackage of the orchestrator.
- **Primary type**: `Coordinator` (concrete struct) implementing the `lifecycle.Coordinator` interface. Constructed via `lifecycle.New(store, emitter, counters)` and held on `*Orchestrator` as `o.lifecycle`.
- **Method set** (intent-revealing verbs, keyed by domain not SQL):
  - `Dispatch(ctx, taskID, instanceID, containerID) error`
  - `Complete(ctx, taskID, Result) error`
  - `Requeue(ctx, taskID, reason string, RequeueKind) error`
  - `Cancel(ctx, taskID) error`
  - `MarkReadyForRetry(ctx, taskID) error`
  - `Recover(ctx, taskID string, containerAlive bool, reason string) error`
- **Result struct**: carries `Status`, `Error`, `PRURL`, `LogTail`, and an optional `ReadingOpt notify.EventOption` populated by the reading pipeline.
- **RequeueKind enum**: `RequeueInterrupted` (emits `task.interrupted`), `RequeueRecovering` (emits `task.recovering`).
- **Dependencies**: `store.Store` for persistence, `notify.Emitter` for events, a small `SlotReleaser` interface (or function value) so the coordinator can release the local orchestrator slot counter — avoids a circular import to `orchestrator.Orchestrator`.
- **Transaction boundary**: dispatch's pending→provisioning→running is wrapped in `Store.WithTx` inside the coordinator; terminal and requeue transitions use their existing single-statement store methods.
- **Testing strategy**: real SQLite (`store.NewSQLite(ctx, t.TempDir()+"/-test.db", migrationsDir)`) plus a capturing `notify.Emitter`. No hand-rolled mock Store for lifecycle tests.
- **Migration order**: recovery.go → monitor.go → dispatch.go → api/task_actions.go. The existing `Store` transition methods remain on the interface during the migration; they become internal-only concerns of `lifecycle/` afterwards. Narrowing or unexporting them is out of scope for this issue.
- **Out of scope**: splitting `Store` into per-domain interfaces (ISP concern).

---

## Phase 1: Package skeleton + `MarkReadyForRetry`

**User stories covered**: issue #32 — "a single module to read to understand the lifecycle" (bootstrap) and migration of the single-call-site `MarkReadyForRetry`.

### What to build

Create the `internal/orchestrator/lifecycle/` package. Declare the full `Coordinator` interface (all six methods), `Result`, and `RequeueKind`, even though only `MarkReadyForRetry` is implemented and wired this phase — the rest return a sentinel "not implemented" until their phases land. Build a real-SQLite test harness (`lifecycle_test.go` helpers) that spins up a tempfile DB with goose migrations and a capturing emitter. Implement `MarkReadyForRetry`: it delegates to `Store.MarkReadyForRetry` and emits no event (the current `markRetryReady` helper in `monitor.go` handles event emission; only the DB write moves this phase). Migrate `monitor.go:395` to call `o.lifecycle.MarkReadyForRetry` instead of `store.MarkReadyForRetry` directly.

### Acceptance criteria

- [ ] `internal/orchestrator/lifecycle/lifecycle.go` exists with `Coordinator` interface, `Result`, `RequeueKind`, and constructor.
- [ ] `internal/orchestrator/lifecycle/lifecycle_test.go` contains a real-SQLite test for `MarkReadyForRetry` (happy path + task-not-found).
- [ ] `Orchestrator` is constructed with a non-nil `Coordinator` in production and in tests.
- [ ] `monitor.go:395` no longer calls `o.store.MarkReadyForRetry` directly.
- [ ] All existing tests still pass (`make test`).

---

## Phase 2: `Recover` (recovery.go)

**User stories covered**: issue #32 — "recovery-path drift bug" (decrement without instance-slot release) and "inspect-error duplicated between monitor.go and recovery.go".

### What to build

Implement `Coordinator.Recover(taskID, containerAlive, reason)`. One entry point that:
- Marks the task `recovering` if it isn't already (idempotent) and emits `task.recovering` with the right message.
- If `containerAlive`, promotes `recovering` → `running` and emits `task.running`.
- If not, requeues the task (delegating to the same transition used by `Requeue(RequeueRecovering)` conceptually, though `Requeue` lands in phase 4 — for this phase the recovery path calls `Store.RequeueTask` directly through the coordinator until phase 4 consolidates).
- Always pairs slot accounting correctly: when a running-at-boot task is requeued, both `decrementRunning` and `releaseInstanceSlot` fire (fixes `recovery.go:150`).

Migrate every transition in `recovery.go` to the coordinator. Extract the inspect-error handling (`monitor.go:97–112` duplicated at `recovery.go:129–144`) into a small helper inside the lifecycle package or a shared orchestrator helper so both callers share one implementation.

### Acceptance criteria

- [ ] `recovery.go` no longer calls `store.UpdateTaskStatus`, `store.RequeueTask`, or `store.ClearTaskAssignment` directly; all transitions go through `o.lifecycle`.
- [ ] The slot-drift bug at `recovery.go:150` is fixed: a recovering-to-requeue transition decrements the local counter **and** the instance slot.
- [ ] Inspect-error handling is not duplicated between `monitor.go` and `recovery.go`.
- [ ] New `lifecycle_test.go` cases cover `Recover(containerAlive=true)` → running and `Recover(containerAlive=false)` → pending, against real SQLite.
- [ ] Existing recovery tests pass (or are updated to reflect the new call path).
- [ ] `make test` green.

---

## Phase 3: `Complete` (monitor.go terminals)

**User stories covered**: issue #32 — "store method selection" and "event emission paired with DB write" for terminal transitions.

### What to build

Implement `Coordinator.Complete(taskID, Result)`. Rejects non-running tasks. Writes `CompleteTask` with `OutputURL`, `PRURL`, cost, and elapsed-time fields derived from the caller's `Result`. Releases the slot and emits `task.completed`, `task.failed`, or `task.needs_input` with `WithContainerStatus(...)` and, for reading-mode completions, `WithReading(...)` via the `Result.ReadingOpt` option.

Migrate both monitor.go call sites (`:159` success/fail/needs-input path and `:420` kill-task failure path). The reading-mode completion flow (`handleReadingCompletion`) still produces the `WithReading` option externally and passes it into `Result` — the coordinator just applies it.

### Acceptance criteria

- [ ] `monitor.go:159` and `:420` no longer call `store.CompleteTask` directly.
- [ ] `Coordinator.Complete` rejects tasks not in `running` with a clear error.
- [ ] Reading-mode completions still emit `task.completed` with `WithReading(...)` populated end-to-end.
- [ ] `lifecycle_test.go` covers: happy-path complete, complete-with-reading, complete-with-failure-and-log-tail, rejection of non-running task.
- [ ] `make test` green; `make test-blackbox` unaffected.

---

## Phase 4: `Requeue` (monitor.go requeue)

**User stories covered**: issue #32 — "output_url lifecycle per HANDOFF Retry output gating" and "task.interrupted vs task.recovering" selection.

### What to build

Implement `Coordinator.Requeue(taskID, reason, RequeueKind)`. Clears `output_url`, decrements both counters, and emits the correct event (`task.interrupted` for `RequeueInterrupted`, `task.recovering` for `RequeueRecovering`). Retrofit phase 2's recovery-requeue path to go through `Requeue(RequeueRecovering)` so there is exactly one implementation.

Migrate `monitor.go:438` (`RequeueTask` after inspect failures / instance loss) and the `ClearTaskAssignment` call at `monitor.go:43` if it is part of a requeue-adjacent flow (otherwise it stays on direct store access until phase 6).

### Acceptance criteria

- [ ] `monitor.go:438` no longer calls `store.RequeueTask` directly.
- [ ] Phase 2's recovery-requeue now routes through `Coordinator.Requeue(RequeueRecovering)`.
- [ ] `output_url` is cleared on every requeue path (verified by test).
- [ ] `lifecycle_test.go` covers both `RequeueKind` variants, `output_url` clearing, and double slot release.
- [ ] `make test` green.

---

## Phase 5: `Dispatch` (dispatch.go)

**User stories covered**: issue #32 — "pending → provisioning → running" reservation and consolidation of the two failure-path `UpdateTaskStatus(failed)` call sites.

### What to build

Implement `Coordinator.Dispatch(taskID, instanceID, containerID)`. Wraps the existing `Store.WithTx` transaction: calls `AssignTask`, then `StartTask`, then `IncrementRunningContainers`. Increments the orchestrator's in-memory running counter after the transaction commits. On failure, the caller still funnels through `Coordinator.Complete` with `Status: failed` (or a new dedicated helper for pre-assignment failures — pick whichever keeps dispatch.go smallest). Migrate both `UpdateTaskStatus(failed)` call sites at `dispatch.go:39` and `:110`, plus the `AssignTask`/`StartTask` pair at `:77`/`:87`.

### Acceptance criteria

- [ ] `dispatch.go` no longer calls `store.AssignTask`, `store.StartTask`, or `store.UpdateTaskStatus` directly.
- [ ] The transactional guarantee (assign + start + increment as one unit) is preserved — concurrent `Dispatch` calls on the same task still can't both succeed.
- [ ] `lifecycle_test.go` covers: happy-path dispatch, concurrent-dispatch conflict, rejection of non-pending task.
- [ ] `make test` green; soak test unaffected (spot-check `make test-soak` optional).

---

## Phase 6: `Cancel` via API + test cleanup

**User stories covered**: issue #32 — "50-method mock Store" reduction and routing `api.CancelTask` through the coordinator.

### What to build

Implement `Coordinator.Cancel(taskID)`. Accepts any non-terminal state, writes `CancelTask`, and emits `task.cancelled`. Update `internal/api/task_actions.go:CancelTask` to take a `Coordinator` (or `CancelOp` function) and call it instead of `store.CancelTask`. Update `NewServer` wiring so the API has access to a coordinator (the orchestrator can expose one; for tests a test coordinator backed by real SQLite).

Shrink `internal/orchestrator/helpers_test.go`'s `mockStore` to only the decision-read methods (`GetTask`, `ListTasks`, etc.) — write-path methods move behind lifecycle tests. Delete state-transition subtests in `dispatch_test.go`, `monitor_test.go`, `recovery_test.go` that are now covered by `lifecycle_test.go`.

### Acceptance criteria

- [ ] `internal/api/task_actions.go:CancelTask` calls `Coordinator.Cancel` (directly or via a function value passed in at handler construction).
- [ ] `mockStore` in `helpers_test.go` implements roughly half the methods it does today (decision reads only).
- [ ] No test in `dispatch_test.go`/`monitor_test.go`/`recovery_test.go` asserts on a transition's persistence via the mock; those live in `lifecycle_test.go`.
- [ ] `make test` green; `make lint` clean.
