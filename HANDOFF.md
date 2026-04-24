# HANDOFF.md

Ledger of cross-PR tradeoffs. Each entry: decision → consequence for downstream work.

## SQLite migration

- **Persistence now uses a local SQLite file, not Postgres/Supabase.** The server opens `BACKFLOW_DATABASE_PATH`, runs SQLite-compatible goose migrations, and keeps DB-backed tests on temporary `-test.db` files. Anything that still assumes `BACKFLOW_DATABASE_URL`, `pgx`, or Postgres-native SQL needs to be updated rather than worked around.
- **Reader duplicate/similarity helpers now call Backlite's own API, not Supabase PostgREST.** Read-mode containers receive `BACKFLOW_API_BASE_URL` (plus `BACKFLOW_API_KEY` when configured) and hit `/api/v1/readings/lookup` and `/api/v1/readings/similar`. If a deployment can't use the default host-gateway URL, set `BACKFLOW_INTERNAL_API_BASE_URL` explicitly.
- **Embedding similarity moved from DB-native vector search to application-side ranking.** Readings store embeddings as JSON text in SQLite and `FindSimilarReadings` computes cosine similarity in Go. This keeps the migration simple and local-first, but very large reading corpora may eventually want an explicit ANN/indexed replacement instead of in-process ranking.

## Duplicate-URL handling for read-mode tasks

- **Duplicate check runs at dispatch, not completion.** Before the orchestrator launches a reader container for a `task_mode=read` task, it calls `store.GetReadingByURL(ctx, task.Prompt)`. If the URL already exists and `task.Force` is false, the task is marked `failed` with `"reading already exists for url ... (id=...); resubmit with force=true to overwrite"` and `task.failed` is emitted — no container, no embedding call, no spend. `Force=true` bypasses the check and dispatches normally, with `UpsertReading` overwriting the existing row on completion. The orchestrator is the source of truth for duplicate detection; the in-container `read-lookup.sh` remains as a best-effort agent hint but is advisory.
- **`GetReadingByURL` added to `Store`.** Selects all columns except `embedding`. The embedding vector is expensive to transport; if a future caller needs it, fetch by id or add a targeted accessor.
- **Completion path uses `UpsertReading` unconditionally, and `CreateReading` is removed from the `Store` interface entirely.** The dispatch-time guard covers non-forced duplicates; the only remaining completion-time write paths are `Force=true` (overwrite by design) and the rare concurrent-dispatch race where two read tasks pass their lookup before either writes (for which "upsert" is the benign outcome). The unique index on `readings.url` remains as a crash-rather-than-corrupt backstop.
- **`force` is now wired on the REST create path.** Older notes that treated `Force` as non-REST-only input are obsolete; callers can now set `force` directly on `POST /api/v1/tasks`.

## Completion artifact ordering

- **Output logs and metadata snapshots are written in two steps.** The orchestrator now persists `container_output.log` first to obtain `output_url`, then completes the task in SQLite, reloads the finished row, and only then writes `task.json` and emits completion-side metadata. This avoids stale "running" snapshots at `/output.json`, at the cost of splitting the writer interface into separate log and metadata calls.

## Retry output gating

- **`/output` and `/output.json` are gated by current-attempt state, not raw file presence.** The API now looks up the task row and only serves persisted artifacts when the task is terminal and `output_url` is still set for the current attempt. `RetryTask` and `RequeueTask` clear `output_url` when they start a new attempt so stale files under `{data_dir}/tasks/{id}/` cannot leak through the API while a retried/requeued attempt is pending, running, or later terminates without producing fresh output. The filesystem path remains per-task rather than per-attempt; if future work needs historical-attempt artifact access, it will need explicit versioning instead of reusing the current endpoints.

## Schema consolidation

- **Migrations collapsed to a single `001_initial_schema.sql`.** The SQLite migration folded every prior migration into one fresh-start schema: `tasks`, `instances`, `api_keys`, `readings`. The orphaned integration tables (`allowed_senders`, `discord_installs`, `discord_task_threads`) and the latent `readings.is_available` column were dropped rather than preserved. `reply_channel` remains on `tasks`; dropping that legacy field is a future-migration concern if it becomes worth the churn. Any new schema change goes in `002_*.sql`.

## Static site removal

- **The repo no longer ships the old static Pages site.** `site/` and the `make deploy-site` target were removed while resolving the SMS/Discord cleanup branch against `main`. If public marketing or legal pages are needed again, recreate them intentionally instead of assuming a Pages deploy still exists.

## AWS runtime removal (issue #5)

- **Only the local Docker runtime remains.** The AWS-specific orchestrator paths, `BACKFLOW_MODE` branches, and `BACKFLOW_ECS_*` / `BACKFLOW_S3_BUCKET` / EC2 env vars were deleted in one PR. `go list -deps ./... | grep aws-sdk-go-v2` is empty. If AWS execution is ever wanted again, it will need to be rebuilt from scratch rather than revived from git history — the Fargate and EC2 runners were deeply entangled with ECS task overrides, SSM, and spot-interruption handling that the simplified orchestrator no longer models.
- **`scripts/setup-aws.sh` and `scripts/teardown-aws.sh` share identifiers via `scripts/aws-resource-names.sh`.** `teardown-aws.sh` was added (wired as `make teardown-aws`) so operators can clean up existing AWS resources; it defaults to dry-run, is idempotent, and continues on error. The teardown script, its helper, and the setup script should all be deleted once the fork has run AWS-free long enough that no lingering cleanup is needed (no hard deadline; tracked here rather than as a follow-up issue).
