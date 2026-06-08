# CLAUDE.md

## What This Is

Backlite is a Go service that runs coding agents (Claude Code or Codex) in ephemeral containers. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

Two task modes: `code` (clone → code → commit → PR) and `review` (PR review with inline comments). The public API defaults to `auto`, and the agent prep stage resolves the prompt to code or review from the GitHub URL.

## Commands

```bash
make build              # Build Go binary to bin/backlite
make run                # Build + run (sources .env if present)
make test               # Unit/integration tests with -tags nocontainers (excludes blackbox; see make test-blackbox)
make lint               # go vet ./...
make test-schema        # Schemathesis fuzz tests against OpenAPI spec (requires docker, goose, schemathesis)
make test-blackbox      # Black-box integration test (builds fake agent, spins up server + DB)
make test-soak          # Soak test (10 min short mode; starts dedicated server on sibling -soak.db)
make test-fake-agent    # Unit tests for the fake agent Docker image
make deps               # go mod tidy
make clean              # Remove bin/ directory
make db-running         # Show running tasks (also: db-pending, db-completed, db-failed)
make docker-agent-build-local        # Agent image (native arch)
make docker-skill-agent-build-local  # Skill-agent image (native arch; opt-in via BACKFLOW_SKILL_AGENT_IMAGE)
make docker-agents-build-local       # Build agent images
make test-skill-agent-entrypoint     # Shell tests for the skill-agent entrypoint
goose -dir migrations status # Show pending/applied migrations
goose -dir migrations up     # Apply the next migration(s)
goose -dir migrations down   # Roll back the last migration
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

## Architecture

Two goroutines: chi REST API on `:8080` + polling orchestrator (5s default). The orchestrator runs agent containers directly on the local Docker host; there is no mode switch.

**Flow:** Client → API → SQLite → Orchestrator → local Docker → Webhooks.

### API endpoints

All JSON responses are wrapped in a `{"data": …}` envelope; errors use `{"error": "…"}`. First-party clients consume the wrapped shape.

- `GET /health` — Health check (root-level, always accessible)
- `GET /debug/stats` — Operational stats: PID, uptime, running tasks, pool metrics (outside `/api/v1/`, bearer-auth protected when API keys are configured)
- `GET /api/v1/health` — Health check (under API prefix; bearer-auth protected when API keys are configured)
- `POST /api/v1/tasks` — Create task
- `GET /api/v1/tasks` — List tasks (query params: `status`, `limit`, `offset`)
- `GET /api/v1/tasks/{id}` — Get task
- `DELETE /api/v1/tasks/{id}` — Cancel task (sets status to `cancelled`)
- `POST /api/v1/tasks/{id}/retry` — Retry a failed/interrupted/cancelled task (atomic, gated by `ready_for_retry` and user retry cap)
- `GET /api/v1/tasks/{id}/logs` — Stream container logs
- `GET /api/v1/tasks/{id}/output` — Return the agent's stdout log (`container_output.log`) persisted to `BACKFLOW_DATA_DIR` after the container exits
- `GET /api/v1/tasks/{id}/output.json` — Return the JSON task metadata snapshot (`task.json`) persisted alongside the output log

### Key modules (`internal/`)

- **api/** — chi router, handlers, JSON responses (envelope helpers in `responses.go`), bearer-token auth middleware (`auth.go`: `BACKFLOW_API_KEY` short-circuit + DB-backed scoped `api_keys` lookup with a 30s `HasAPIKeys` cache), `LogFetcher` interface, `NewTask` shared task-creation helper, and shared `CancelTask` / `RetryTask` action helpers
- **orchestrator/** — Poll loop (`orchestrator.go`), dispatch (`dispatch.go`), monitoring (`monitor.go`), recovery (`recovery.go`). Subpackages: `docker/` (local Docker container management), `outputs/` (filesystem writer for agent logs + task metadata), `lifecycle/` (`Coordinator` owning task state transitions, slot accounting, and paired event emission — callers invoke domain verbs like `Dispatch`/`Complete`/`Requeue`/`Cancel` instead of selecting Store methods; on a `Complete` write failure the slot is **not** released and no event is emitted, so the next monitor tick can retry against the still-`running` row), `chain/` (atomic self-review chained-task creation — exposes a `ChainTx` callback that the lifecycle Coordinator runs in the same SQLite transaction as the parent's `CompleteTask`, so the parent commit and child INSERT either both land or both roll back), `imagerouter/` (selects which agent image to use given task harness + configured images).
- **store/** — `Store` interface + SQLite (`database/sql`, goose migrations)
- **models/** — `Task` structs with status enums. `Task.AgentImage` records which Docker image the orchestrator used. `Task.ParentTaskID` is an optional pointer to the task that spawned this one (retry chains, follow-ups, sub-tasks); the column has a self-referential FK with `ON DELETE SET NULL`. `FindFirstURL` / `InferReviewMode` auto-detect review mode when a prompt's first URL is a GitHub PR URL.
- **config/** — Env-var config (`BACKFLOW_*` prefix). `BACKFLOW_API_KEY` enables single-token API auth; otherwise `api_keys` in SQLite can back authenticated API/debug requests. `TaskDefaults(taskMode)` returns resolved defaults. `Apply(task, overrides)` fills zero-value fields using `*bool` overrides (nil = use default, non-nil = use pointed value).
- **notify/** — `Notifier` interface, `WebhookNotifier` (HTTP POST, 3 retries, event filtering), `NoopNotifier`, `EventBus` (async fan-out delivery via buffered channel), `NewEvent` constructor with `EventOption` functional options. `Event` carries `TaskMode` and `ParentTaskID` when set.
- **debug/** — `/debug/stats` handler: PID, uptime, running task count, database handle metrics
- **backup/** — Local SQLite backup manager. `Manager.MaybeSchedule(ctx)` is invoked from each orchestrator tick; when enabled and the latest valid artifact is older than the configured interval, it spawns a single background goroutine that uses the SQLite online-backup API, gzip-compresses the snapshot, decompresses + `PRAGMA integrity_check`s it, then atomically renames into place and writes a sidecar with size, sha256, and finalization time. Subsequent ticks recompute the sha256 of the latest candidate before trusting it; mismatches fall back to the next-older valid artifact.
- **skillcontract/** — Embedded JSON Schema validator (`schema.json`) for skill-agent `status.json` payloads. Tests walk every `docker/skill-agent/skills/*/examples/status.json` fixture and assert the deliberately broken negative fixture fails. Used by the skill-agent build to keep skill bundles' contract test fixtures honest.

### Fake agent (`test/blackbox/fake-agent/`)

Minimal Alpine image used by black-box and soak tests. Reads `FAKE_OUTCOME` env var to simulate outcomes: `success`, `slow_success`, `fail`, `needs_input`, `timeout`, `crash`. Writes `status.json` and emits `BACKFLOW_STATUS_JSON:` just like the real agent. Does not create `container_output.log` (soak tasks set `save_agent_output: false`).

### Soak test (`test/soak/`)

Long-running resource leak detector. Submits tasks at intervals, collects RSS, pool stats, and container counts, then analyzes for memory growth and container accumulation. Run via `make test-soak` (10-min short mode). It derives a sibling `-soak.db` path from `BACKFLOW_DATABASE_PATH`, starts a dedicated Backlite subprocess against that database, truncates the soak tables there, and prunes stale containers at start and end. The wrapper script (`scripts/test-soak.sh`) warns before truncating and asks for confirmation.

### Agent containers

The orchestrator picks an image per dispatch via `internal/orchestrator/imagerouter`:

- **`docker/agent/`** — Original agent. Node.js 24 + Claude Code CLI + Codex CLI + git + gh. `entrypoint.sh` (~611 lines) does prep stage → clone → CLAUDE.md inject → run agent (in-container retry up to 3 attempts) → commit → push → create PR → optional self-review. Supports both `claude_code` and `codex` harnesses in code and review modes.
- **`docker/skill-agent/`** — Opt-in via `BACKFLOW_SKILL_AGENT_IMAGE`. **Claude Code only** (codex tasks are rejected with a clear error). Skill bundles bake at `/opt/backflow/skills/{auto,code,review}/`. `entrypoint.sh` is ~95 lines: validate env, fetch S3-offloaded fields, gh auth, copy the requested skill into `~/.claude/skills/<mode>/`, exec `claude` with a starter prompt, then notarize `cost_usd` from the harness stream-json into the agent-written `status.json` (or synthesize a fallback failure status if missing/unparsable). No harness branching, no in-container retry, no prep stage. `auto` is the only mode that branches at runtime — its skill inspects the prompt and dispatches to `code` or `review`, so the entrypoint installs both sub-bundles alongside it. Source-tree skill bundles live at `docker/skill-agent/skills/{auto,code,review}/`.

**Image routing** (`internal/orchestrator/imagerouter`):
1. `task.harness == "claude_code"` and `cfg.SkillAgentImage != ""` → `SkillAgentImage`
2. Otherwise → `cfg.AgentImage`

When `BACKFLOW_SKILL_AGENT_IMAGE` is unset, behavior is identical to the standard agent image path. When set, only claude_code tasks reroute — codex tasks continue to use the existing image. If the orchestrator routes a codex task to the skill-agent image (which it shouldn't), the entrypoint fails fast.

### Statuses

- **Task:** `pending` → `provisioning` → `running` → `completed` | `failed` | `interrupted` | `cancelled` | `recovering` → `pending` | `running` | `completed` | `failed`

### Success determination

Success is the agent's call, not the harness's. `monitor.handleCompletion` requires `complete=true` in the agent-written `status.json` to mark a task `completed`; any other state (including `complete=false` with a clean `exit_code=0`) becomes `failed`. This keeps skill-authored failure branches (e.g. "no repo URL in prompt") from slipping through as success when the underlying CLI happened to exit cleanly. `needs_input=true` short-circuits to `failed` with the `task.needs_input` event regardless of `complete`.

### Chained self-review

`POST /api/v1/tasks` accepts an optional `self_review: true`. When a code task with `self_review=true` completes successfully and produced a PR URL, the orchestrator atomically creates a child review task in the same SQLite transaction as the parent's completion. The child has:

- `task_mode = review`
- `parent_task_id` set to the parent's ID
- `max_budget_usd = 2.00` (flat — independent of parent budget; standalone review tasks keep using the request's `max_budget_usd`)
- harness inherited from the parent
- prompt synthesized from the parent's PR URL + parent prompt for context

`task.created` fires for the child after the parent's `task.completed`. Subsequent webhook events for the child (`task.running`, `task.completed`, …) include `parent_task_id` so downstream automation can correlate. If the child insert fails the parent's COMPLETE rolls back too — atomicity is by SQLite transaction in `lifecycle.Coordinator.Complete`'s `ChainTx` hook, with the planning logic in `internal/orchestrator/chain`.

### Webhook events

`task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`, `task.cancelled`, `task.retry`

## Skill-based agent image (opt-in)

`BACKFLOW_SKILL_AGENT_IMAGE` opts a deployment into the `docker/skill-agent/` image (claude_code-only). See **Agent containers** above for the routing rule and the absent-from-this-image components (prep stage and in-container retry loop). Skill bundles live in the source tree at `docker/skill-agent/skills/{auto,code,review}/`; the entrypoint copies the requested bundle into `~/.claude/skills/<mode>/` at start so Claude Code's native skill loader picks it up. The `auto` bundle dispatches at runtime to `code` or `review`, so the entrypoint installs both sub-bundles alongside it. The `status.json` contract is enforced by the Go validator in `internal/skillcontract`, which embeds `schema.json` and walks every `docker/skill-agent/skills/*/examples/status.json` fixture in tests. Skills are not a user-facing extension point — operators do not supply or override skill content per task.

## Local SQLite backups

Enabled by default. Each orchestrator tick calls `backup.Manager.MaybeSchedule(ctx)`; the manager first prunes aged artifacts and stale temp files (see below), then exits early if disabled, already running, or the latest valid backup is younger than `BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC`. Otherwise it launches a single background goroutine that:

1. Opens the configured database read-side and runs the SQLite online backup API (`*sqlite.Backup` from `modernc.org/sqlite`) into a temp file.
2. Gzip-compresses the temp file to a `.sqlite.gz.tmp` sibling.
3. Decompresses to a verification temp file and runs `PRAGMA integrity_check`.
4. Hashes the compressed bytes, `os.Rename`s the gzip into place, then atomically writes a `.meta.json` sidecar containing `file_name`, `created_at` (scheduled), `finalized_at` (post-rename), `sha256`, and `size_bytes`.

Artifact filenames are `backlite-YYYYMMDDTHHMMSSZ.sqlite.gz` (UTC). Age comparisons use `finalized_at` so a backup that takes longer than the interval does not immediately appear stale and trigger a continuous loop. Validity requires a structurally-correct sidecar **and** a recomputed sha256 that matches; corrupted artifacts are skipped and the scheduler falls back to the next-older valid one.

Retention pruning runs synchronously at the top of each `MaybeSchedule` tick and deletes:

- Finalized artifacts older than `BACKFLOW_LOCAL_BACKUP_RETENTION_SEC`, paired with their `.meta.json` sidecars. The newest valid artifact is always preserved regardless of age. Setting retention to `0` disables pruning.
- Stale temp files (`.sqlite.tmp`, `.sqlite.gz.tmp`, `.sqlite.gz.tmp.verify`, `.meta.json.tmp`) whose mtime is older than a 1-hour grace.
- Orphan `.meta.json` sidecars whose `.sqlite.gz` artifact has already been removed.

Each delete logs `pruned local sqlite backup …` at `Info` with `file_name`, `age_seconds`, and a `reason` of `age`, `stale_temp`, or `orphan_metadata`. Per-file delete failures log `Error` and the loop continues.

Operator visibility lives on `/debug/stats` under the `backup` key: `enabled`, `directory`, `interval_seconds`, `retention_seconds`, `worker_state` (`idle`/`running`), `latest_artifact` (the sidecar metadata), `last_success_at`, `last_error_at`, `last_error_message`, and a `recent_errors` ring of the last 5 entries (each tagged with `phase: "backup"` or `"prune"`). Skipped ticks log at `Debug` with `reason=disabled|already_running|not_due` so they don't spam logs at default levels.

Failures (e.g. integrity check fails, disk full, source DB locked beyond `busy_timeout`, retention prune errors) are logged and recorded in `recent_errors`, but do not affect health checks (`/health`, `/api/v1/health`) or task orchestration. Backup work runs concurrently with task orchestration but `MaybeSchedule` enforces single-flight via a mutex.

Env vars (see `internal/config/config.go` for current defaults):

- `BACKFLOW_LOCAL_BACKUP_ENABLED` — toggle the worker (default on)
- `BACKFLOW_LOCAL_BACKUP_DIR` — output directory; supports `~` expansion
- `BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC` — minimum spacing between successful backups
- `BACKFLOW_LOCAL_BACKUP_RETENTION_SEC` — age (seconds) past which finalized backups are pruned; `0` disables pruning

Restore is manual: stop the server, copy the chosen `.sqlite.gz` aside, `gunzip` it, optionally re-run `PRAGMA integrity_check`, replace the file at `BACKFLOW_DATABASE_PATH`, and restart.

## Output storage

When a task's container exits and `save_agent_output` is enabled, the orchestrator writes two files under `{BACKFLOW_DATA_DIR}/tasks/{id}/`:

- `container_output.log` — raw agent stdout, served by `GET /api/v1/tasks/{id}/output`
- `task.json` — JSON snapshot of the task row, served by `GET /api/v1/tasks/{id}/output.json`

Writes are atomic (`*.tmp` sibling + `os.Rename`), so consumers never observe a half-written file. `BACKFLOW_DATA_DIR` defaults to `./data`; see config for current defaults.

## Harnesses

- **`claude_code`** — Claude Code CLI. Requires `ANTHROPIC_API_KEY` or Max subscription credentials.
- **`codex`** — OpenAI Codex CLI. Requires `OPENAI_API_KEY`.

Configured per-task via the `harness` field or globally via `BACKFLOW_DEFAULT_HARNESS`.

PR comments include actual cost for `claude_code` (extracted from `total_cost_usd` in stream-json output). Codex CLI doesn't report cost in dollars — only raw token counts via `--json` — so cost is omitted for `codex` harness runs.

## API auth

- `BACKFLOW_API_KEY` — Optional single bearer token for API and debug access in small deployments
- `api_keys` — SQLite-backed bearer tokens with named scopes (`tasks:read`, `tasks:write`, `health:read`, `stats:read`) and optional expiration

When API keys are configured, bearer auth applies to `/api/v1/*` and `/debug/stats`. Root `/health` remains public.

## Documentation guidelines

Do not record default values for config or env vars in documentation. Defaults change frequently and docs drift silently. Instead, point to the source (`internal/config/config.go`) or say "see config for current defaults."

## Input validation

Environment variable keys passed via the `env_vars` field must match POSIX naming rules (`^[A-Za-z_][A-Za-z0-9_]*$`) and must not override reserved system keys (e.g. `ANTHROPIC_API_KEY`, `GITHUB_TOKEN`, `TASK_ID`, `BACKFLOW_API_KEY`, `BACKFLOW_API_BASE_URL`). See `reservedEnvVarKeys` in `internal/models/task.go` for the full list.

Secrets (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GITHUB_TOKEN`) are passed via `--env-file` rather than inline in the `docker run` command string so they stay out of process listings and `docker inspect`.

## Design patterns

- Interface abstractions (`Store`, `Notifier`, `LogFetcher`) for testability
- Polling over events for simplicity
- Local Docker via `exec` — no remote orchestration layer
- ULID task IDs with `bf_` prefix
- Zerolog structured logging

## Database

SQLite. Tables: `tasks` and `api_keys`. See `docs/schema.md` for the full column-level schema. Migrations are managed by [goose](https://github.com/pressly/goose) and live in `migrations/`. The store implementation is in `internal/store/sqlite.go` using `database/sql`. Set `BACKFLOW_DATABASE_PATH` to the local database path.

The schema was collapsed to a single `001_initial_schema.sql` baseline; `002_parent_task_id.sql` added `tasks.parent_task_id` plus `idx_tasks_parent_task_id`. Any new schema change starts at the next numeric prefix.

Migration workflow:

```bash
goose -dir migrations status
goose -dir migrations up
goose -dir migrations down
```

Create new migrations in `migrations/` with the next numeric prefix, `-- +goose Up`, and `-- +goose Down`.

## Documentation

Additional docs in `docs/`:
- `schema.md` — Database schema (tables, columns, indexes, status lifecycles)
- `self-hosting.md` — Single-host deployment walkthrough
- `adrs/` — Architecture Decision Records (historical; see individual files for current status)

The OpenAPI 3.0 spec lives at `api/openapi.yaml`. `make test-schema` runs Schemathesis fuzz tests against it.
