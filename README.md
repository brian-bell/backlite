# Backlite

Agent orchestrator that runs coding agents (Claude Code or Codex) in ephemeral containers. POST a task (repo + prompt), get back a branch with commits and a PR. The current runtime is local Docker plus a local SQLite database.

Also supports a `read` task mode that runs a dedicated reader image against a URL, summarizes it, embeds the TL;DR, and stores the result in a `readings` table for similarity search. See [CLAUDE.md](CLAUDE.md#reading-mode). The same Go binary serves a small React reading-library SPA at `/` so saved readings can be browsed in a browser.

## Prerequisites

- Go 1.25+
- Node.js 20+ and npm (for the bundled web app — `make build` compiles it into the Go binary)
- Docker
- SQLite
- `jq` (for helper scripts)

## Local Development

For a from-scratch single-host setup, see [docs/self-hosting.md](docs/self-hosting.md).

```bash
cp .env.example .env
# Edit .env — at minimum set BACKFLOW_DATABASE_PATH, ANTHROPIC_API_KEY, and GITHUB_TOKEN
```

```bash
make build          # Build web bundle (web/dist) + Go binary at bin/backlite
make run            # Build + run (auto-sources .env)
make test           # Run all Go tests with -tags nocontainers (no cache)
make lint           # go vet
make deps           # go mod tidy
make clean          # Remove bin/
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

DB-backed tests use temporary SQLite files ending in `-test.db`.

### Web app

```bash
make web-deps       # npm install
make web-generate   # Regenerate web/src/generated/api.d.ts from api/openapi.yaml
make web-dev        # Vite dev server (point it at a running Backlite via the Auth token form)
make web-build      # tsc + vite build (also runs as part of `make build`)
make web-test       # Vitest suite
```

The build output lives in `web/dist/` (gitignored). The Go server statically serves it at `/*`, with SPA fallback to `index.html` for client-side routes; set `BACKFLOW_WEB_DIR` to a different directory if you serve a prebuilt bundle from elsewhere.

```bash
make test-blackbox                # End-to-end: builds fake agent, starts server + DB, runs happy-path
make test-soak                    # Resource leak detector (10 min; starts dedicated server on sibling -soak.db)
make test-fake-agent              # Unit tests for the fake agent image
make test-schema                  # Schemathesis fuzz tests against OpenAPI spec
make test-skill-agent-entrypoint  # Shell-level e2e tests for the skill-agent container entrypoint
make test-reader-fetch-extract    # Hermetic shell test for the reader's pre-fetch + extraction pipeline
```

## Submitting Tasks

`prompt` is the only required field. Include a GitHub URL in the prompt — the agent container's prep stage infers `repo_url`, `target_branch`, and the concrete `task_mode` (code or review) from it. Scripts pass through only explicitly-set options; the server applies defaults for anything omitted. Use `--pr` / `--no-pr` to override the server's `BACKFLOW_DEFAULT_CREATE_PR` setting.

```bash
# Simple task (creates PR by default)
./scripts/create-task.sh "Fix the login bug in https://github.com/org/repo"

# Skip PR creation
./scripts/create-task.sh "Fix the login bug in https://github.com/org/repo" --no-pr

# With options
./scripts/create-task.sh "Add unit tests to https://github.com/org/repo" \
  --pr-title "Add tests" --budget 15 --model claude-sonnet-4-6 \
  --context "Focus on the auth module" \
  --claude-md "Always use table-driven tests" \
  --effort medium --self-review

# Prompt from a file (file should contain a GitHub URL)
./scripts/create-task.sh --plan plan.md

# With env vars
./scripts/create-task.sh "Fix bug in https://github.com/org/repo" \
  --env "GOPRIVATE=github.com/org/*"
```

### PR Reviews

```bash
./scripts/review-pr.sh https://github.com/org/repo/pull/42
./scripts/review-pr.sh https://github.com/org/repo/pull/42 --prompt "Focus on security issues"
./scripts/review-pr.sh https://github.com/org/repo/pull/42 --harness codex --budget 5
```

### Reading Mode

Submits a URL to a dedicated reader image, which fetches the page, drafts a TL;DR, and persists a row in the `readings` table (with an embedding for similarity search). HTML pages also have their raw bytes and a Readability-derived markdown rendering captured under `BACKFLOW_DATA_DIR/readings/<id>/` and exposed via `GET /api/v1/readings/{id}/content` and `/content/raw`. Requires `BACKFLOW_READER_IMAGE` and `OPENAI_API_KEY`.

```bash
./scripts/read-url.sh https://example.com/article
./scripts/read-url.sh https://example.com/article --force          # overwrite existing row
./scripts/read-url.sh https://example.com/article --budget 0.5
```

### Direct API

```bash
# Create a code task (URL must be in the prompt)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{"prompt": "Fix the bug in https://github.com/org/repo"}'

# Review a PR (auto-detected from the PR URL in the prompt)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{"prompt": "Review https://github.com/org/repo/pull/42"}'

# Read mode (explicit; URL goes in the prompt)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{"task_mode": "read", "prompt": "https://example.com/article"}'

# Codex harness (requires OPENAI_API_KEY)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{"prompt": "Fix the bug in https://github.com/org/repo", "harness": "codex"}'
```

## API Reference

The full OpenAPI 3.0 spec lives at [`api/openapi.yaml`](api/openapi.yaml). `make test-schema` runs Schemathesis fuzz tests against it. JSON responses are wrapped in a `{"data": …}` envelope (errors use `{"error": "…"}`).

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/tasks` | Create a task |
| `GET` | `/api/v1/tasks` | List tasks (`?status=`, `?limit=`, `?offset=`) |
| `GET` | `/api/v1/tasks/{id}` | Get task details |
| `DELETE` | `/api/v1/tasks/{id}` | Cancel a task |
| `POST` | `/api/v1/tasks/{id}/retry` | Retry a failed/cancelled/interrupted task |
| `GET` | `/api/v1/tasks/{id}/logs` | Container logs (`?tail=100`) |
| `GET` | `/api/v1/tasks/{id}/output` | Persisted agent stdout log |
| `GET` | `/api/v1/tasks/{id}/output.json` | Persisted task metadata snapshot |
| `GET` | `/api/v1/readings` | List stored readings (`?limit=`, `?offset=`); requires `readings:read` scope |
| `GET` | `/api/v1/readings/{id}` | Reading detail (TL;DR, summary, tags, connections); requires `readings:read` scope |
| `GET` | `/api/v1/readings/{id}/content` | Extracted markdown for the reading (HTML-derived); 404 when content was not captured; requires `readings:read` scope |
| `GET` | `/api/v1/readings/{id}/content/raw` | Raw captured bytes with the recorded `Content-Type`; 404 when content was not captured; requires `readings:read` scope |
| `GET` | `/api/v1/readings/lookup` | Exact-URL duplicate check (public — used by reader containers) |
| `POST` | `/api/v1/readings/similar` | Semantic similarity search over stored readings (public) |
| `GET` | `/api/v1/health` | Health check |
| `GET` | `/debug/stats` | Operational stats (PID, uptime, running tasks, pool metrics) |
| `GET` | `/*` | Reading-library SPA from `BACKFLOW_WEB_DIR` (defaults to `./web/dist`) |

### Task Request Fields

The only required field is `prompt`. For code/review tasks, the prompt must contain a GitHub URL — the prep stage extracts `repo_url`, `target_branch`, and the concrete `task_mode`. The user-facing `task_mode` enum is `auto` (default) or `read`; code/review are inferred and not user-settable.

| Field | Type | Description |
|-------|------|-------------|
| `prompt` | string | **Required.** Agent instructions; for code/review must include a GitHub URL |
| `task_mode` | string | `auto` (default) or `read`. Code vs review is inferred from the prompt |
| `harness` | string | `claude_code` or `codex` (omit to use server default) |
| `model` | string | Model override (per-harness; see server config) |
| `effort` | string | `low`, `medium`, `high`, or `xhigh` |
| `create_pr` | bool | Create a PR on completion (omit to use server default) |
| `self_review` | bool | When `true` and the code task creates a PR, the orchestrator atomically chains a follow-up review task with a flat $2 budget and `parent_task_id` pointing at this task |
| `force` | bool | Read mode only: overwrite an existing `readings` row for the URL |
| `pr_title` | string | Custom PR title |
| `pr_body` | string | Custom PR body |
| `max_budget_usd` | float | Budget cap in USD |
| `max_runtime_sec` | int | Runtime cap in seconds |
| `max_turns` | int | Max conversation turns |
| `context` | string | Additional context appended to prompt |
| `claude_md` | string | Extra CLAUDE.md content injected into the repo |
| `allowed_tools` | []string | Restrict agent tool access |
| `env_vars` | map | Extra env vars passed to the container (keys must be POSIX-valid; system keys like `ANTHROPIC_API_KEY` are reserved) |
| `save_agent_output` | bool | Persist agent output for the `/output` endpoints (omit to use server default) |

## Monitoring and Operations

```bash
# Tasks by status
make db-running
make db-pending
make db-completed
make db-failed

# Task details
curl -s http://localhost:8080/api/v1/tasks/{id} | jq .

# Container logs
curl -s 'http://localhost:8080/api/v1/tasks/{id}/logs?tail=100'

# Health check
curl -s http://localhost:8080/api/v1/health

# Operational stats (PID, uptime, running tasks, pool metrics)
curl -s http://localhost:8080/debug/stats | jq .
```

### Task Lifecycle

`pending` -> `provisioning` -> `running` -> `completed` | `failed` | `interrupted` | `cancelled`

Interrupted/failed tasks can enter `recovering` -> re-queued as `pending`.

### Database

SQLite. Migrations are managed by [goose](https://github.com/pressly/goose) in `migrations/`. Auto-runs on startup. Configured via `BACKFLOW_DATABASE_PATH`.

```bash
make db-running                             # Show running tasks
make db-pending                             # Show pending tasks
make db-completed                           # Show completed tasks
make db-failed                              # Show failed tasks
sqlite3 "$BACKFLOW_DATABASE_PATH" ".tables"
sqlite3 "$BACKFLOW_DATABASE_PATH" "SELECT id, status, created_at FROM tasks ORDER BY created_at DESC LIMIT 10;"
```

To add a migration: create a new file in `migrations/` (e.g. `002_add_column.sql`) with `-- +goose Up` and `-- +goose Down` sections.

## Docker Images

```bash
make docker-agent-build-local         # Agent image (claude_code + codex)
make docker-reader-build-local        # Reader image (for task_mode=read)
make docker-skill-agent-build-local   # Skill-agent image (claude_code-only; opt-in)
make docker-agents-build-local        # Build all three agent images
```

Backlite runs agent containers directly against the local Docker daemon; there is no remote orchestration runtime.

The three agent images coexist: `docker/agent/` and `docker/reader/` are the originals, and `docker/skill-agent/` is a thin claude_code-only image that expresses each task mode as a Claude Code skill bundle. Set `BACKFLOW_SKILL_AGENT_IMAGE=<image>` to opt in — claude_code tasks reroute to the skill-agent image (regardless of mode); codex tasks continue to use the existing images. Unset to roll back instantly. See [CLAUDE.md](CLAUDE.md#agent-containers--three-coexisting-images) for the full routing rule.

## Configuration

All config via environment variables or `.env` file. See `.env.example` for the full list.

### General

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Required for `claude_code` harness |
| `OPENAI_API_KEY` | Required for `codex` harness; also required for reading-mode completion (embedding the TL;DR) |
| `GITHUB_TOKEN` | For cloning private repos and creating PRs |
| `BACKFLOW_LISTEN_ADDR` | Server listen address |
| `BACKFLOW_DATABASE_PATH` | SQLite database path (required) |
| `BACKFLOW_DATA_DIR` | Filesystem root for persisted task artifacts (`container_output.log`, `task.json`) |
| `BACKFLOW_MAX_CONTAINERS` | Concurrency cap (≤ `MaxLocalContainers` in `internal/config/config.go`) |
| `BACKFLOW_POLL_INTERVAL_SEC` | Orchestrator poll interval (seconds) |

See `internal/config/config.go` and `.env.example` for the full surface and current defaults.

### Reading Mode

| Variable | Description |
|----------|-------------|
| `BACKFLOW_READER_IMAGE` | Docker image used for `task_mode=read` containers |
| `BACKFLOW_DEFAULT_READ_MAX_BUDGET` | Budget cap for reading tasks |
| `BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC` | Runtime cap for reading tasks |
| `BACKFLOW_DEFAULT_READ_MAX_TURNS` | Max turns for reading tasks |
| `BACKFLOW_INTERNAL_API_BASE_URL` | Optional override for the Backlite API base URL that reader containers use for duplicate/similarity lookups |

### Agent Defaults

Defaults are set in `internal/config/config.go` and can be overridden via env vars. See `.env.example` for current values.

| Variable | Description |
|----------|-------------|
| `BACKFLOW_DEFAULT_HARNESS` | `claude_code` or `codex` |
| `BACKFLOW_DEFAULT_CLAUDE_MODEL` | Default model for Claude Code |
| `BACKFLOW_DEFAULT_CODEX_MODEL` | Default model for Codex |
| `BACKFLOW_DEFAULT_EFFORT` | Reasoning effort (`low`, `medium`, `high`, `xhigh`) |
| `BACKFLOW_DEFAULT_MAX_BUDGET` | Budget cap (USD) |
| `BACKFLOW_DEFAULT_MAX_RUNTIME_SEC` | Runtime cap (seconds) |
| `BACKFLOW_DEFAULT_MAX_TURNS` | Max conversation turns |
| `BACKFLOW_DEFAULT_CREATE_PR` | Create PR by default |
| `BACKFLOW_DEFAULT_SELF_REVIEW` | Self-review by default |
| `BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT` | Save agent output by default |
| `BACKFLOW_AGENT_IMAGE` | Docker image for agent containers (see config for default) |
| `BACKFLOW_SKILL_AGENT_IMAGE` | Optional opt-in: when set, routes every `claude_code` task to a skill-bundle image instead of `BACKFLOW_AGENT_IMAGE` / `BACKFLOW_READER_IMAGE`. Codex tasks are unaffected. See [CLAUDE.md](CLAUDE.md#skill-based-agent-image-opt-in). |
| `BACKFLOW_MAX_USER_RETRIES` | Max user-initiated retries per task (see config for default) |
| `BACKFLOW_CONTAINER_CPUS` | CPU cores per container |
| `BACKFLOW_CONTAINER_MEMORY_GB` | Memory (GB) per container |

### Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_WEBHOOK_URL` | | Webhook endpoint URL |
| `BACKFLOW_WEBHOOK_EVENTS` | all | Comma-separated event filter |

Events: `task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`, `task.cancelled`, `task.retry`

### Email summary delivery (read mode)

Optional Resend integration that emails a structured summary of every completed `task_mode=read` task. Read mode + `claude_code` harness + skill-agent image only — codex read tasks and non-read modes do not send email. All three vars must be set together; partial config blocks startup. See [docs/resend-setup.md](docs/resend-setup.md) for sender-domain DNS setup.

| Variable | Description |
|----------|-------------|
| `BACKFLOW_RESEND_API_KEY` | Resend API key (`re_…`) |
| `BACKFLOW_NOTIFY_EMAIL_FROM` | Verified sender address — must use a Resend-verified domain |
| `BACKFLOW_NOTIFY_EMAIL_TO` | Recipient inbox |

### Web app

| Variable | Description |
|----------|-------------|
| `BACKFLOW_WEB_DIR` | Directory of the prebuilt web bundle served at `/*` (defaults to `./web/dist`). Leave the directory empty to disable the SPA route. |

### Local SQLite backups

Enabled by default. The server runs a single background worker from the orchestrator tick that takes a consistent online SQLite snapshot, gzip-compresses and verifies it, and writes it into a configurable directory alongside a `.meta.json` sidecar (`file_name`, `created_at`, `finalized_at`, `sha256`, `size_bytes`). Artifacts are named `backlite-YYYYMMDDTHHMMSSZ.sqlite.gz`. The latest artifact's sha256 is recomputed each tick before it is trusted; corrupted artifacts are skipped and the scheduler falls back to the previous valid one. Backup failures are logged and do not affect health checks or task orchestration.

| Variable | Description |
|----------|-------------|
| `BACKFLOW_LOCAL_BACKUP_ENABLED` | Toggle the local backup worker |
| `BACKFLOW_LOCAL_BACKUP_DIR` | Output directory (supports `~` expansion) |
| `BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC` | Minimum spacing between successful backups |

To restore: stop the server, `gunzip backlite-...sqlite.gz`, optionally `sqlite3 file.sqlite "PRAGMA integrity_check;"`, copy the result over the file at `BACKFLOW_DATABASE_PATH`, and restart.
