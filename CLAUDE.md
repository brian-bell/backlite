# CLAUDE.md

## What This Is

Backlite is a Go service that runs coding agents (Claude Code or Codex) in ephemeral containers. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

Three task modes: `code` (default: clone → code → commit → PR), `review` (PR review with inline comments), and `read` (fetch a URL, summarize it via a reader agent, embed the TL;DR, store the result in the `readings` table for later similarity search).

## Commands

```bash
make build              # Build to bin/backlite
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
make docker-server-build-local       # Server image (native arch)
make docker-reader-build-local       # Reader image (native arch)
make docker-skill-agent-build-local  # Skill-agent image (native arch; opt-in via BACKFLOW_SKILL_AGENT_IMAGE)
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

- **api/** — chi router, handlers, JSON responses, `LogFetcher` interface, `NewTask` shared task-creation helper, `CancelTask` and `RetryTask` shared action helpers
- **orchestrator/** — Poll loop (`orchestrator.go`), dispatch (`dispatch.go`), monitoring (`monitor.go`, including `handleReadingCompletion` for read-mode tasks), recovery (`recovery.go`). Subpackages: `docker/` (local Docker container management), `outputs/` (filesystem writer for agent logs + task metadata), `lifecycle/` (`Coordinator` owning task state transitions, slot accounting, paired event emission, and the `ChainTx` hook used for atomic chained-task creation), `imagerouter/` (single-function `Resolve(task, cfg) string` that picks the docker image for a dispatch — claude_code goes to `SkillAgentImage` when set, otherwise read mode → `ReaderImage`, otherwise `AgentImage`), `chain/` (`Plan(parent)` decides whether to chain a self-review task and what shape the child takes; works with the `ChainTx` hook so parent COMPLETE + child INSERT happen in one SQLite tx).
- **skillcontract/** — JSON-schema-style validator for the `status.json` payload that a skill-based agent writes. `Validate(b []byte) error` checks required fields, types, and enums. `make test` walks `docker/skill-agent/skills/*/examples/status.json` so drift between SKILL.md instructions and orchestrator parsing is caught.
- **store/** — `Store` interface + SQLite (`database/sql`, goose migrations). Includes `UpsertReading` / `GetReadingByURL` / `FindSimilarReadings` for the `readings` table.
- **models/** — `Task` and `Reading` (+ `Connection`) structs with status enums. `Task.AgentImage` records which Docker image the orchestrator used (read tasks get `ReaderImage`, others get the default agent image). `FindFirstURL` / `InferReviewMode` auto-detect review mode when a prompt's first URL is a GitHub PR URL.
- **embeddings/** — Thin `Embedder` interface (`Embed(ctx, text) ([]float32, error)`) with an `OpenAIEmbedder` HTTP client (no SDK). Used by the orchestrator to embed a reading's final TL;DR before writing the `readings` row.
- **config/** — Env-var config (`BACKFLOW_*` prefix). `BACKFLOW_API_KEY` enables single-token API auth; otherwise `api_keys` in SQLite can back authenticated API/debug requests. `TaskDefaults(taskMode)` returns resolved defaults — for `read` mode it swaps in `ReaderImage` plus the `BACKFLOW_DEFAULT_READ_MAX_*` caps. `Apply(task, overrides)` fills zero-value fields using `*bool` overrides (nil = use default, non-nil = use pointed value)
- **notify/** — `Notifier` interface, `WebhookNotifier` (HTTP POST, 3 retries, event filtering), `NoopNotifier`, `EventBus` (async fan-out delivery via buffered channel), `NewEvent` constructor with `EventOption` functional options (including `WithReading` for read-mode completion events). `Event` carries `TaskMode` plus optional reading fields (`TLDR`, `NoveltyVerdict`, `Tags`, `Connections`) populated only for read-task completion events.
- **debug/** — `/debug/stats` handler: PID, uptime, running task count, database handle metrics

### Fake agent (`test/blackbox/fake-agent/`)

Minimal Alpine image used by black-box and soak tests. Reads `FAKE_OUTCOME` env var to simulate outcomes: `success`, `slow_success`, `fail`, `needs_input`, `timeout`, `crash`. Writes `status.json` and emits `BACKFLOW_STATUS_JSON:` just like the real agent. Does not create `container_output.log` (soak tasks set `save_agent_output: false`).

### Soak test (`test/soak/`)

Long-running resource leak detector. Submits tasks at intervals, collects RSS, pool stats, and container counts, then analyzes for memory growth and container accumulation. Run via `make test-soak` (10-min short mode). It derives a sibling `-soak.db` path from `BACKFLOW_DATABASE_PATH`, starts a dedicated Backlite subprocess against that database, truncates the soak tables there, and prunes stale containers at start and end. The wrapper script (`scripts/test-soak.sh`) warns before truncating and asks for confirmation.

### Agent containers — three coexisting images

Three docker images coexist; the orchestrator picks one per dispatch via `internal/orchestrator/imagerouter`:

- **`docker/agent/`** — Original agent. Node.js 24 + Claude Code CLI + Codex CLI + git + gh. `entrypoint.sh` (~611 lines) does prep stage → clone → CLAUDE.md inject → run agent (in-container retry up to 3 attempts) → commit → push → create PR → optional self-review. Supports both `claude_code` and `codex` harnesses in code and review modes.
- **`docker/reader/`** — Read-mode agent. Same base, `reader-entrypoint.sh` runs the harness against a URL and emits a structured reading JSON.
- **`docker/skill-agent/`** — Opt-in via `BACKFLOW_SKILL_AGENT_IMAGE`. **Claude Code only** (codex tasks are rejected with a clear error). Skill bundles bake at `/opt/backflow/skills/{code,review,read}/`. `entrypoint.sh` is ~95 lines: validate env, fetch S3-offloaded fields, gh auth, copy the requested skill into `~/.claude/skills/<mode>/`, exec `claude` with a starter prompt, then notarize `cost_usd` from the harness stream-json into the agent-written `status.json` (or synthesize a fallback failure status if missing/unparsable). No mode branching, no harness branching, no in-container retry, no prep stage. `status_writer.sh` and `reader_helpers.sh` do not exist on this image. Source-tree skill bundles live at `docker/skill-agent/skills/{code,review,read}/`.

**Image routing** (`internal/orchestrator/imagerouter`):
1. `task.harness == "claude_code"` and `cfg.SkillAgentImage != ""` → `SkillAgentImage`
2. `task.task_mode == "read"` and `cfg.ReaderImage != ""` → `ReaderImage`
3. Otherwise → `cfg.AgentImage`

When `BACKFLOW_SKILL_AGENT_IMAGE` is unset, behavior is identical to before. When set, only claude_code tasks reroute — codex tasks continue to use the existing images. If the orchestrator routes a codex task to the skill-agent image (which it shouldn't), the entrypoint fails fast.

### Chained self-review

`POST /api/v1/tasks` accepts an optional `self_review: true`. When a code task with `self_review=true` completes successfully and produced a PR URL, the orchestrator atomically creates a child review task in the same SQLite transaction as the parent's completion. The child has:

- `task_mode = review`
- `parent_task_id` set to the parent's ID
- `max_budget_usd = 2.00` (flat — independent of parent budget; standalone review tasks keep using the request's `max_budget_usd`)
- harness inherited from the parent
- prompt synthesized from the parent's PR URL + parent prompt for context

`task.created` fires for the child after the parent's `task.completed`. Subsequent webhook events for the child (`task.running`, `task.completed`, …) include `parent_task_id` so downstream automation can correlate. If the child insert fails the parent's COMPLETE rolls back too — atomicity is by SQLite transaction in `lifecycle.Coordinator.Complete`'s `ChainTx` hook, with the planning logic in `internal/orchestrator/chain`.

### Statuses

- **Task:** `pending` → `provisioning` → `running` → `completed` | `failed` | `interrupted` | `cancelled` | `recovering` → `pending` | `running` | `completed` | `failed`

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

`BACKFLOW_SKILL_AGENT_IMAGE` opts a deployment into the new `docker/skill-agent/` image (claude_code-only). See **Agent containers — three coexisting images** above for the routing rule and the absent-from-this-image components (`status_writer.sh`, `reader_helpers.sh`, prep stage, in-container retry loop). Skill bundles live in the source tree at `docker/skill-agent/skills/{code,review,read}/`; the entrypoint copies the requested bundle into `~/.claude/skills/<mode>/` at start so Claude Code's native skill loader picks it up. Skills are not a user-facing extension point — operators do not supply or override skill content per task.

The `tasks` table carries a `force` boolean column. REST callers can set `force` on `POST /api/v1/tasks`; `Force=true` bypasses the dispatch-time duplicate check and allows an existing reading row to be overwritten on completion.

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

SQLite. Tables: `tasks`, `api_keys`, `readings`. See `docs/schema.md` for the full column-level schema. Migrations are managed by [goose](https://github.com/pressly/goose) and live in `migrations/`. The store implementation is in `internal/store/sqlite.go` using `database/sql`. Set `BACKFLOW_DATABASE_PATH` to the local database path.

The schema has been collapsed to a single `001_initial_schema.sql` — any new schema change starts at `002_*.sql`.

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
- `adrs/` — Architecture Decision Records (historical; see individual files for current status)
