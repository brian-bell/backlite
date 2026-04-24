# Backlite

Agent orchestrator that runs coding agents (Claude Code or Codex) in ephemeral containers. POST a task (repo + prompt), get back a branch with commits and a PR. The current runtime is local Docker plus a local SQLite database.

Also supports a `read` task mode that runs a dedicated reader image against a URL, summarizes it, embeds the TL;DR, and stores the result in a `readings` table for similarity search. See [CLAUDE.md](CLAUDE.md#reading-mode).

## Prerequisites

- Go 1.25+
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
make build          # Compile to bin/backlite
make run            # Build + run (auto-sources .env)
make test           # Run all tests with -tags nocontainers (no cache)
make lint           # go vet
make deps           # go mod tidy
make clean          # Remove bin/
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

DB-backed tests use temporary SQLite files ending in `-test.db`.

```bash
make test-blackbox      # End-to-end: builds fake agent, starts server + DB, runs happy-path
make test-soak          # Resource leak detector (10 min; starts dedicated server on sibling -soak.db)
make test-fake-agent    # Unit tests for the fake agent image
make test-schema        # Schemathesis fuzz tests against OpenAPI spec
```

## Submitting Tasks

Scripts pass through only explicitly-set options; the server applies defaults for anything omitted. Use `--pr` / `--no-pr` to override the server's `BACKFLOW_DEFAULT_CREATE_PR` setting.

```bash
# Simple task (creates PR by default)
./scripts/create-task.sh https://github.com/org/repo "Fix the login bug"

# Skip PR creation
./scripts/create-task.sh https://github.com/org/repo "Fix the login bug" --no-pr

# With options
./scripts/create-task.sh https://github.com/org/repo "Add unit tests" \
  --pr-title "Add tests" --budget 15 --model claude-sonnet-4-6 \
  --branch my-feature --target-branch develop \
  --context "Focus on the auth module" \
  --claude-md "Always use table-driven tests" \
  --effort medium --self-review

# Prompt from a file
./scripts/create-task.sh https://github.com/org/repo --plan plan.md

# With env vars
./scripts/create-task.sh https://github.com/org/repo "Fix bug" \
  --env "GOPRIVATE=github.com/org/*"
```

### PR Reviews

```bash
./scripts/review-pr.sh https://github.com/org/repo/pull/42
./scripts/review-pr.sh https://github.com/org/repo/pull/42 --prompt "Focus on security issues"
./scripts/review-pr.sh https://github.com/org/repo/pull/42 --harness codex --budget 5
```

### Direct API

```bash
# Create a coding task (server applies defaults for create_pr, effort, etc.)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "repo_url": "https://github.com/org/repo",
    "prompt": "Fix the bug"
  }'

# Review a PR (auto-detects review mode from PR URL in prompt)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Review https://github.com/org/repo/pull/42"
  }'

# Explicit review mode
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "Review https://github.com/org/repo/pull/42"
  }'

# Codex harness (requires OPENAI_API_KEY)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "repo_url": "https://github.com/org/repo",
    "prompt": "Fix the bug",
    "harness": "codex"
  }'
```

## API Reference

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
| `GET` | `/api/v1/health` | Health check |
| `GET` | `/debug/stats` | Operational stats (PID, uptime, running tasks, pool metrics) |

### Task Request Fields

| Field | Type | Description |
|-------|------|-------------|
| `repo_url` | string | **Required.** Repository URL |
| `prompt` | string | **Required for code mode.** Agent instructions |
| `task_mode` | string | `code`, `review`, or `read`; auto-detected from PR URLs in prompt when unset |
| `harness` | string | `codex` or `claude_code` |
| `model` | string | Model override (per-harness; see server config) |
| `effort` | string | `low`, `medium`, `high`, or `xhigh` |
| `branch` | string | Working branch name |
| `target_branch` | string | Target branch |
| `create_pr` | bool | Create a PR on completion (omit to use server default) |
| `self_review` | bool | Agent self-reviews the PR after creation (omit to use server default) |
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
make docker-agent-build-local    # Single-arch agent image
make docker-agent-build          # Multi-arch buildx (amd64+arm64)

make docker-reader-build-local   # Single-arch reader image (for task_mode=read)
make docker-reader-build         # Multi-arch buildx

make docker-server-build-local   # Single-arch server image
make docker-server-build         # Multi-arch buildx
```

Backlite runs agent containers directly against the local Docker daemon; there is no remote orchestration runtime. A legacy `make teardown-aws` target exists to clean up AWS resources from older deploys — see `scripts/teardown-aws.sh` (dry-run by default; pass `ARGS="--yes"` to actually delete).

## Configuration

All config via environment variables or `.env` file. See `.env.example` for the full list.

### General

| Variable | Default | Description |
|----------|---------|-------------|
| `ANTHROPIC_API_KEY` | | Required |
| `OPENAI_API_KEY` | | Required for `codex` harness; also required for reading-mode completion (embedding the TL;DR) |
| `GITHUB_TOKEN` | | For cloning private repos and creating PRs |
| `BACKFLOW_LISTEN_ADDR` | `:8080` | Server listen address |
| `BACKFLOW_DATABASE_PATH` | | SQLite database path |
| `BACKFLOW_POLL_INTERVAL_SEC` | `5` | Orchestrator poll interval (seconds) |

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
| `BACKFLOW_MAX_USER_RETRIES` | Max user-initiated retries per task (see config for default) |
| `BACKFLOW_CONTAINER_CPUS` | CPU cores per container |
| `BACKFLOW_CONTAINER_MEMORY_GB` | Memory (GB) per container |

### Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_WEBHOOK_URL` | | Webhook endpoint URL |
| `BACKFLOW_WEBHOOK_EVENTS` | all | Comma-separated event filter |

Events: `task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`, `task.cancelled`, `task.retry`
