# CLAUDE.md

## What This Is

Backlite is a Go service that runs coding agents (Claude Code or Codex) in ephemeral containers. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

Three task modes: `code` (default: clone → code → commit → PR), `review` (PR review with inline comments), and `read` (fetch a URL, summarize it via a reader agent, embed the TL;DR, store the result in the `readings` table for later similarity search).

## Commands

```bash
make build              # Build web bundle + Go binary to bin/backlite
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
make web-deps           # Install web/ npm dependencies
make web-generate       # Regenerate web/src/generated/api.d.ts from api/openapi.yaml
make web-dev            # Run the Vite dev server against a local Backlite instance
make web-build          # Generate API types + tsc + vite build (also runs as part of make build)
make web-test           # Vitest suite for the web app
make docker-agent-build-local        # Agent image (native arch)
make docker-reader-build-local       # Reader image (native arch)
make docker-skill-agent-build-local  # Skill-agent image (native arch; opt-in via BACKFLOW_SKILL_AGENT_IMAGE)
make docker-agents-build-local       # Build all three agent images
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

All JSON responses are wrapped in a `{"data": …}` envelope; errors use `{"error": "…"}`. The web app and any first-party clients consume the wrapped shape.

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
- `GET /api/v1/readings` — Paginated newest-first list of stored readings (query params: `limit`, `offset`); requires `readings:read` scope when API keys are configured
- `GET /api/v1/readings/{id}` — Single reading detail (full TL;DR, summary, tags, connections); requires `readings:read` scope
- `GET /api/v1/readings/lookup` — Exact-URL duplicate check (public route — no auth — used by reader containers and the orchestrator's dispatch-time guard)
- `POST /api/v1/readings/similar` — Semantic similarity search over stored `readings` (public route, cosine similarity in Go over JSON-encoded embeddings)
- `GET /*` — Static SPA bundle from `BACKFLOW_WEB_DIR` (defaults to `./web/dist`). Falls back to `index.html` for client-side routes; `/api/*`, `/debug/*`, and `/health` are reserved and never served by the SPA handler. Disabled when the directory is empty.

### Key modules (`internal/`)

- **api/** — chi router, handlers, JSON responses (envelope helpers in `responses.go`), bearer-token auth middleware (`auth.go`: `BACKFLOW_API_KEY` short-circuit + DB-backed scoped `api_keys` lookup with a 30s `HasAPIKeys` cache), web SPA static handler (`static.go`), `LogFetcher` interface, `NewTask`/`NewReadTask` shared task-creation helpers, `CancelTask` and `RetryTask` shared action helpers, and the readings list/detail handlers consumed by the web app
- **orchestrator/** — Poll loop (`orchestrator.go`), dispatch (`dispatch.go`), monitoring (`monitor.go`, including `handleReadingCompletion` for read-mode tasks), recovery (`recovery.go`). Subpackages: `docker/` (local Docker container management), `outputs/` (filesystem writer for agent logs + task metadata), `lifecycle/` (`Coordinator` owning task state transitions, slot accounting, and paired event emission — callers invoke domain verbs like `Dispatch`/`Complete`/`Requeue`/`Cancel` instead of selecting Store methods; on a `Complete` write failure the slot is **not** released and no event is emitted, so the next monitor tick can retry against the still-`running` row), `chain/` (atomic self-review chained-task creation — exposes a `ChainTx` callback that the lifecycle Coordinator runs in the same SQLite transaction as the parent's `CompleteTask`, so the parent commit and child INSERT either both land or both roll back), `imagerouter/` (selects which agent image to use given task harness + mode + configured images).
- **store/** — `Store` interface + SQLite (`database/sql`, goose migrations). Includes `UpsertReading` / `GetReadingByURL` / `FindSimilarReadings` for the `readings` table.
- **models/** — `Task` and `Reading` (+ `Connection`) structs with status enums. `Task.AgentImage` records which Docker image the orchestrator used (read tasks get `ReaderImage`, others get the default agent image). `Task.ParentTaskID` is an optional pointer to the task that spawned this one (retry chains, follow-ups, sub-tasks); the column has a self-referential FK with `ON DELETE SET NULL`. `FindFirstURL` / `InferReviewMode` auto-detect review mode when a prompt's first URL is a GitHub PR URL.
- **embeddings/** — Thin `Embedder` interface (`Embed(ctx, text) ([]float32, error)`) with an `OpenAIEmbedder` HTTP client (no SDK). Used by the orchestrator to embed a reading's final TL;DR before writing the `readings` row.
- **config/** — Env-var config (`BACKFLOW_*` prefix). `BACKFLOW_API_KEY` enables single-token API auth; otherwise `api_keys` in SQLite can back authenticated API/debug requests. `TaskDefaults(taskMode)` returns resolved defaults — for `read` mode it swaps in `ReaderImage` plus the `BACKFLOW_DEFAULT_READ_MAX_*` caps. `Apply(task, overrides)` fills zero-value fields using `*bool` overrides (nil = use default, non-nil = use pointed value). `Load()` enforces an all-or-nothing gate on the email-notification trio (`BACKFLOW_RESEND_API_KEY`, `BACKFLOW_NOTIFY_EMAIL_FROM`, `BACKFLOW_NOTIFY_EMAIL_TO`): setting any one of them without the other two fails startup.
- **notify/** — `Notifier` interface, `WebhookNotifier` (HTTP POST, 3 retries, event filtering), `NoopNotifier`, `EventBus` (async fan-out delivery via buffered channel), `NewEvent` constructor with `EventOption` functional options (including `WithReading` for read-mode completion events). `Event` carries `TaskMode`, `ParentTaskID` (when set), plus optional reading fields (`TLDR`, `NoveltyVerdict`, `Tags`, `Connections`) populated only for read-task completion events.
- **debug/** — `/debug/stats` handler: PID, uptime, running task count, database handle metrics
- **backup/** — Local SQLite backup manager. `Manager.MaybeSchedule(ctx)` is invoked from each orchestrator tick; when enabled and the latest valid artifact is older than the configured interval, it spawns a single background goroutine that uses the SQLite online-backup API, gzip-compresses the snapshot, decompresses + `PRAGMA integrity_check`s it, then atomically renames into place and writes a sidecar with size, sha256, and finalization time. Subsequent ticks recompute the sha256 of the latest candidate before trusting it; mismatches fall back to the next-older valid artifact.
- **skillcontract/** — Embedded JSON Schema validator (`schema.json`) for skill-agent `status.json` payloads. Tests walk every `docker/skill-agent/skills/*/examples/status.json` fixture and assert the deliberately broken negative fixture fails. Used by the skill-agent build to keep skill bundles' contract test fixtures honest.

### Web app (`web/`)

Reading-library SPA: React 19 + TypeScript + Vite + TanStack Query, with `openapi-typescript` generating types directly off `api/openapi.yaml` and `openapi-fetch` issuing the HTTP calls. The bundle is built to `web/dist/` (gitignored) and served by the Go binary's static handler at `/*`; `make build` runs `web-build` first so a single binary ships the SPA. The user pastes a bearer token into the topbar form (persisted in `localStorage` under `backlite.bearerToken`) and the API calls attach it as `Authorization: Bearer …`. Routes today: `/` (paginated reading list, page size 20) and `/readings/:id` (TL;DR, summary, tags/keywords/people/orgs, connections). Vitest specs live next to the source (`App.test.tsx`).

### Fake agent (`test/blackbox/fake-agent/`)

Minimal Alpine image used by black-box and soak tests. Reads `FAKE_OUTCOME` env var to simulate outcomes: `success`, `slow_success`, `fail`, `needs_input`, `timeout`, `crash`. Writes `status.json` and emits `BACKFLOW_STATUS_JSON:` just like the real agent. Does not create `container_output.log` (soak tasks set `save_agent_output: false`).

### Soak test (`test/soak/`)

Long-running resource leak detector. Submits tasks at intervals, collects RSS, pool stats, and container counts, then analyzes for memory growth and container accumulation. Run via `make test-soak` (10-min short mode). It derives a sibling `-soak.db` path from `BACKFLOW_DATABASE_PATH`, starts a dedicated Backlite subprocess against that database, truncates the soak tables there, and prunes stale containers at start and end. The wrapper script (`scripts/test-soak.sh`) warns before truncating and asks for confirmation.

### Agent containers — three coexisting images

Three docker images coexist; the orchestrator picks one per dispatch via `internal/orchestrator/imagerouter`:

- **`docker/agent/`** — Original agent. Node.js 24 + Claude Code CLI + Codex CLI + git + gh. `entrypoint.sh` (~611 lines) does prep stage → clone → CLAUDE.md inject → run agent (in-container retry up to 3 attempts) → commit → push → create PR → optional self-review. Supports both `claude_code` and `codex` harnesses in code and review modes.
- **`docker/reader/`** — Read-mode agent. Same base, `reader-entrypoint.sh` runs the harness against a URL and emits a structured reading JSON.
- **`docker/skill-agent/`** — Opt-in via `BACKFLOW_SKILL_AGENT_IMAGE`. **Claude Code only** (codex tasks are rejected with a clear error). Skill bundles bake at `/opt/backflow/skills/{auto,code,review,read}/`. `entrypoint.sh` is ~95 lines: validate env, fetch S3-offloaded fields, gh auth, copy the requested skill into `~/.claude/skills/<mode>/`, exec `claude` with a starter prompt, then notarize `cost_usd` from the harness stream-json into the agent-written `status.json` (or synthesize a fallback failure status if missing/unparsable). No harness branching, no in-container retry, no prep stage. `auto` is the only mode that branches at runtime — its skill inspects the prompt and dispatches to `code` or `review`, so the entrypoint installs both sub-bundles alongside it. `status_writer.sh` and `reader_helpers.sh` do not exist on this image. Source-tree skill bundles live at `docker/skill-agent/skills/{auto,code,review,read}/`.

**Image routing** (`internal/orchestrator/imagerouter`):
1. `task.harness == "claude_code"` and `cfg.SkillAgentImage != ""` → `SkillAgentImage`
2. `task.task_mode == "read"` and `cfg.ReaderImage != ""` → `ReaderImage`
3. Otherwise → `cfg.AgentImage`

When `BACKFLOW_SKILL_AGENT_IMAGE` is unset, behavior is identical to before. When set, only claude_code tasks reroute — codex tasks continue to use the existing images. If the orchestrator routes a codex task to the skill-agent image (which it shouldn't), the entrypoint fails fast.

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

## Reading mode

When a task's `task_mode` is `read`, the orchestrator selects `BACKFLOW_READER_IMAGE` instead of the default agent image. The reader container fetches the URL in the prompt, drafts a summary, and emits structured JSON (url/title/tldr/tags/connections/novelty_verdict/etc.) to `status.json`.

**At dispatch** (before the reader container launches), the orchestrator looks up the URL via `store.GetReadingByURL`. If the row already exists and `!task.Force`, the task is marked `failed` with `"reading already exists for url X (id=Y); resubmit with force=true to overwrite"` and no container is started. This avoids spending reader-container minutes and LLM tokens on a URL that's already captured — and means the orchestrator, not the agent, is the source of truth for duplicate detection. The in-container `read-lookup.sh` script still exists as a best-effort hint during the agent's session but is no longer authoritative. If the DB lookup itself errors, dispatch fails through the generic error path and the task is marked failed with the DB error.

On completion, the orchestrator's `handleReadingCompletion` helper (in `internal/orchestrator/monitor.go`) runs synchronously:

1. Parses the reading-specific fields off `ContainerStatus`.
2. If the agent's `novelty_verdict` is `"duplicate"` and `!task.Force`, short-circuits with no write (agent noticed a dup mid-run; preserve the existing row).
3. Calls `embeddings.Embedder.Embed(ctx, tldr)` to embed the final TL;DR (re-embedded by the orchestrator, not reused from the agent — the agent's draft TL;DR can be refined after similarity lookup).
4. Writes the row via `store.UpsertReading`.
5. Emits `task.completed` with `WithReading(tldr, verdict, tags, connections)`.

If the embedding API call or the DB write fails, the task is marked `failed` rather than silently `completed`, and `task.failed` is emitted. If `embedder` is nil (no `OPENAI_API_KEY` configured), reading tasks fail at completion.

The reading agent image and reader-side shell scripts live in `docker/reader/`:

- `reader-entrypoint.sh` — Image entrypoint: runs the harness, extracts JSON via `reader_helpers.sh`, writes `status.json` via `status_writer.sh`.
- `read-embed.sh` — Embeds text via OpenAI `text-embedding-3-small`. Used by the agent to embed a draft TL;DR for similarity search.
- `read-similar.sh` — Semantic similarity search: embeds input text, calls Backlite's `/api/v1/readings/similar` endpoint.
- `read-lookup.sh` — Exact-URL duplicate check via Backlite's `/api/v1/readings/lookup` endpoint.
- `reader_helpers.sh` — JSON extraction helpers (pulls the first JSON object from the agent transcript).
- `status_writer.sh` — Shared helper for writing `status.json`.

Reading-mode env vars:

- `BACKFLOW_READER_IMAGE` — Docker image for reading-mode containers
- `BACKFLOW_DEFAULT_READ_MAX_BUDGET` / `BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC` / `BACKFLOW_DEFAULT_READ_MAX_TURNS` — Tighter defaults applied by `TaskDefaults("read")`
- `OPENAI_API_KEY` — Required for the orchestrator's embeddings client (and for the reader container's `read-embed.sh`)
- `BACKFLOW_INTERNAL_API_BASE_URL` — Optional override for the Backlite API base URL used by reader containers; defaults to `http://host.docker.internal:<listen-port>`

## Skill-based agent image (opt-in)

`BACKFLOW_SKILL_AGENT_IMAGE` opts a deployment into the `docker/skill-agent/` image (claude_code-only). See **Agent containers — three coexisting images** above for the routing rule and the absent-from-this-image components (`status_writer.sh`, `reader_helpers.sh`, prep stage, in-container retry loop). Skill bundles live in the source tree at `docker/skill-agent/skills/{auto,code,review,read}/`; the entrypoint copies the requested bundle into `~/.claude/skills/<mode>/` at start so Claude Code's native skill loader picks it up. The `auto` bundle dispatches at runtime to `code` or `review`, so the entrypoint installs both sub-bundles alongside it. The `status.json` contract is enforced by the Go validator in `internal/skillcontract`, which embeds `schema.json` and walks every `docker/skill-agent/skills/*/examples/status.json` fixture in tests. Skills are not a user-facing extension point — operators do not supply or override skill content per task.

The `tasks` table carries a `force` boolean column. REST callers can set `force` on `POST /api/v1/tasks`; `Force=true` bypasses the dispatch-time duplicate check and allows an existing reading row to be overwritten on completion.

## Local SQLite backups

Enabled by default. Each orchestrator tick calls `backup.Manager.MaybeSchedule(ctx)`; the manager exits early if disabled, already running, or the latest valid backup is younger than `BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC`. Otherwise it launches a single background goroutine that:

1. Opens the configured database read-side and runs the SQLite online backup API (`*sqlite.Backup` from `modernc.org/sqlite`) into a temp file.
2. Gzip-compresses the temp file to a `.sqlite.gz.tmp` sibling.
3. Decompresses to a verification temp file and runs `PRAGMA integrity_check`.
4. Hashes the compressed bytes, `os.Rename`s the gzip into place, then atomically writes a `.meta.json` sidecar containing `file_name`, `created_at` (scheduled), `finalized_at` (post-rename), `sha256`, and `size_bytes`.

Artifact filenames are `backlite-YYYYMMDDTHHMMSSZ.sqlite.gz` (UTC). Age comparisons use `finalized_at` so a backup that takes longer than the interval does not immediately appear stale and trigger a continuous loop. Validity requires a structurally-correct sidecar **and** a recomputed sha256 that matches; corrupted artifacts are skipped and the scheduler falls back to the next-older valid one.

Failures (e.g. integrity check fails, disk full, source DB locked beyond `busy_timeout`) are logged and do not affect health checks or task orchestration. Backup work runs concurrently with task orchestration but `MaybeSchedule` enforces single-flight via a mutex.

Env vars (see `internal/config/config.go` for current defaults):

- `BACKFLOW_LOCAL_BACKUP_ENABLED` — toggle the worker (default on)
- `BACKFLOW_LOCAL_BACKUP_DIR` — output directory; supports `~` expansion
- `BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC` — minimum spacing between successful backups

Restore is manual: stop the server, copy the chosen `.sqlite.gz` aside, `gunzip` it, optionally re-run `PRAGMA integrity_check`, replace the file at `BACKFLOW_DATABASE_PATH`, and restart.

## Email summary delivery (read mode, opt-in)

When `BACKFLOW_RESEND_API_KEY`, `BACKFLOW_NOTIFY_EMAIL_FROM`, and `BACKFLOW_NOTIFY_EMAIL_TO` are all set, the orchestrator propagates them into skill-agent containers as `RESEND_API_KEY`, `NOTIFY_EMAIL_FROM`, and `NOTIFY_EMAIL_TO` (via the same `--env-file` channel as other secrets — never on the command line). The read skill's `send-email.sh` (`docker/skill-agent/skills/read/`) reads `~/workspace/status.json`, formats a structured plain-text body (URL, title, novelty verdict, tags, keywords, people, orgs, TL;DR, summary markdown, connections, task ID — each section omitted when its source field is empty), and POSTs a single message to `https://api.resend.com/emails`. Subject is the page title (falls back to URL hostname when title is empty). Email send is advisory: missing env vars, missing `status.json`, or a Resend API failure each log to stderr and `exit 0` so they cannot block task completion. Scope is read mode + claude_code + skill-agent image only; codex read tasks (which route to `docker/reader/`) and non-read modes do not send email. Operator setup (Resend account, sender-domain DNS, env vars) is in `docs/resend-setup.md`. The three env-var keys are reserved and cannot be overridden via per-task `env_vars`.

## Output storage

When a task's container exits and `save_agent_output` is enabled, the orchestrator writes two files under `{BACKFLOW_DATA_DIR}/tasks/{id}/`:

- `container_output.log` — raw agent stdout, served by `GET /api/v1/tasks/{id}/output`
- `task.json` — JSON snapshot of the task row, served by `GET /api/v1/tasks/{id}/output.json`

Writes are atomic (`*.tmp` sibling + `os.Rename`), so readers never observe a half-written file. `BACKFLOW_DATA_DIR` defaults to `./data`; see config for current defaults.

## Harnesses

- **`claude_code`** — Claude Code CLI. Requires `ANTHROPIC_API_KEY` or Max subscription credentials.
- **`codex`** — OpenAI Codex CLI. Requires `OPENAI_API_KEY`.

Configured per-task via the `harness` field or globally via `BACKFLOW_DEFAULT_HARNESS`.

PR comments include actual cost for `claude_code` (extracted from `total_cost_usd` in stream-json output). Codex CLI doesn't report cost in dollars — only raw token counts via `--json` — so cost is omitted for `codex` harness runs.

## API auth

- `BACKFLOW_API_KEY` — Optional single bearer token for API and debug access in small deployments
- `api_keys` — SQLite-backed bearer tokens with named scopes (`tasks:read`, `tasks:write`, `readings:read`, `health:read`, `stats:read`) and optional expiration

When API keys are configured, bearer auth applies to `/api/v1/*` (except the reader-container endpoints below) and `/debug/stats`. The static SPA bundle at `/*`, root `/health`, `GET /api/v1/readings/lookup`, and `POST /api/v1/readings/similar` remain public — the latter two are called by reader containers from inside the orchestrator's docker network and are not gated.

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

SQLite. Tables: `tasks`, `api_keys`, `readings`. See `docs/schema.md` for the full column-level schema. Migrations are managed by [goose](https://github.com/pressly/goose) and live in `migrations/`. The store implementation is in `internal/store/sqlite.go` using `database/sql`. Set `BACKFLOW_DATABASE_PATH` to the local database path.

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
- `resend-setup.md` — Operator setup for the read skill's email summary delivery (Resend account, sender-domain DNS, env vars)
- `adrs/` — Architecture Decision Records (historical; see individual files for current status)

The OpenAPI 3.0 spec lives at `api/openapi.yaml`. `make test-schema` runs Schemathesis fuzz tests against it.
