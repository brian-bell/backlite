# HANDOFF.md

Ledger of cross-PR tradeoffs. Each entry: decision → consequence for downstream work.

## Duplicate-URL handling for read-mode tasks

- **Duplicate check runs at dispatch, not completion.** Before the orchestrator launches a reader container for a `task_mode=read` task, it calls `store.GetReadingByURL(ctx, task.Prompt)`. If the URL already exists and `task.Force` is false, the task is marked `failed` with `"reading already exists for url ... (id=...); resubmit with force=true to overwrite"` and `task.failed` is emitted — no container, no embedding call, no spend. `Force=true` bypasses the check and dispatches normally, with `UpsertReading` overwriting the existing row on completion. The orchestrator is the source of truth for duplicate detection; the in-container `read-lookup.sh` remains as a best-effort agent hint but is advisory.
- **`GetReadingByURL` added to `Store`.** Selects all columns except `embedding`. The embedding vector is expensive to transport; if a future caller needs it, fetch by id or add a targeted accessor.
- **Completion path uses `UpsertReading` unconditionally, and `CreateReading` is removed from the `Store` interface entirely.** The dispatch-time guard covers non-forced duplicates; the only remaining completion-time write paths are `Force=true` (overwrite by design) and the rare concurrent-dispatch race where two read tasks pass their lookup before either writes (for which "upsert" is the benign outcome). The unique index on `readings.url` remains as a crash-rather-than-corrupt backstop.
- **`force` is now wired on the REST create path.** Older notes that treated `Force` as non-REST-only input are obsolete; callers can now set `force` directly on `POST /api/v1/tasks`.

## Completion artifact ordering

- **Output logs and metadata snapshots are written in two steps.** The orchestrator now persists `container_output.log` first to obtain `output_url`, then completes the task in Postgres, reloads the finished row, and only then writes `task.json` / `task_metadata.json` and emits completion-side metadata. This avoids stale "running" snapshots at `/output.json` and in S3, at the cost of splitting the writer interface into separate log and metadata calls.

## Retry output gating

- **`/output` and `/output.json` are gated by current-attempt state, not raw file presence.** The API now looks up the task row and only serves persisted artifacts when the task is terminal and `output_url` is still set for the current attempt. `RetryTask` and `RequeueTask` clear `output_url` when they start a new attempt so stale files under `{data_dir}/tasks/{id}/` cannot leak through the API while a retried/requeued attempt is pending, running, or later terminates without producing fresh output. The filesystem path remains per-task rather than per-attempt; if future work needs historical-attempt artifact access, it will need explicit versioning instead of reusing the current endpoints.
