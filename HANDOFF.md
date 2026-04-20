# HANDOFF.md

Ledger of cross-PR tradeoffs. Each entry: decision → consequence for downstream work.

## Duplicate-URL handling for read-mode tasks

- **Duplicate check runs at dispatch, not completion.** Before the orchestrator launches a reader container for a `task_mode=read` task, it calls `store.GetReadingByURL(ctx, task.Prompt)`. If the URL already exists and `task.Force` is false, the task is marked `failed` with `"reading already exists for url ... (id=...); resubmit with force=true to overwrite"` and `task.failed` is emitted — no container, no embedding call, no spend. `Force=true` bypasses the check and dispatches normally, with `UpsertReading` overwriting the existing row on completion. The orchestrator is the source of truth for duplicate detection; the in-container `read-lookup.sh` remains as a best-effort agent hint but is advisory.
- **`GetReadingByURL` added to `Store`.** Selects all columns except `embedding`. The embedding vector is expensive to transport; if a future caller needs it, fetch by id or add a targeted accessor.
- **Completion path uses `UpsertReading` unconditionally, and `CreateReading` is removed from the `Store` interface entirely.** The dispatch-time guard covers non-forced duplicates; the only remaining completion-time write paths are `Force=true` (overwrite by design) and the rare concurrent-dispatch race where two read tasks pass their lookup before either writes (for which "upsert" is the benign outcome). The unique index on `readings.url` remains as a crash-rather-than-corrupt backstop.
- **API still lacks a `force` wire field.** The Discord `/backflow read` command already accepts `force` (default false). REST callers cannot set `Force` until the create endpoint is extended.

## SMS removal docs cleanup

- **Operator/public docs were updated in the SMS-removal slice, but legacy schema/history references remain until the schema-drop slice.** `.env.example`, Fly secret sync, Discord setup docs, and site/legal copy now reflect that SMS/Twilio support is gone. `docs/schema.md`, migrations, and historical review notes still mention `reply_channel` / `allowed_senders` because those database artifacts remain in place until the later migration that drops them; clean those references up when the schema actually changes.
