# Backlite

Agent orchestrator that runs coding agents (Claude Code or Codex) in ephemeral containers. POST a code task (repo + prompt) or a PR review task, get back commits, PRs, and review comments. The current runtime is local Docker plus a local SQLite database.

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
make build          # Build Go binary at bin/backlite
make run            # Build + run (auto-sources .env)
make test           # Run all Go tests with -tags nocontainers (no cache)
make lint           # go vet
make deps           # go mod tidy
make clean          # Remove bin/
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

DB-backed tests use temporary SQLite files ending in `-test.db`.

```bash
make test-blackbox                # End-to-end: builds fake agent, starts server + DB, runs happy-path
make test-soak                    # Resource leak detector (10 min; starts dedicated server on sibling -soak.db)
make test-fake-agent              # Unit tests for the fake agent image
make test-schema                  # Schemathesis fuzz tests against OpenAPI spec
make test-s3-backup               # MinIO-backed integration test for S3 backup uploads
make test-skill-agent-entrypoint  # Shell-level e2e tests for the skill-agent container entrypoint
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
| `GET` | `/api/v1/health` | Health check |
| `GET` | `/debug/stats` | Operational stats (PID, uptime, running tasks, pool metrics) |

### Task Request Fields

The only required field is `prompt`. The prompt must contain a GitHub URL — the prep stage extracts `repo_url`, `target_branch`, and whether the task is code or review. The user-facing `task_mode` value is `auto` by default; code/review are inferred from the prompt.

| Field | Type | Description |
|-------|------|-------------|
| `prompt` | string | **Required.** Agent instructions; for code/review must include a GitHub URL |
| `task_mode` | string | `auto` by default. Code vs review is inferred from the prompt |
| `harness` | string | `claude_code` or `codex` (omit to use server default) |
| `model` | string | Model override (per-harness; see server config) |
| `effort` | string | `low`, `medium`, `high`, or `xhigh` |
| `create_pr` | bool | Create a PR on completion (omit to use server default) |
| `self_review` | bool | When `true` and the code task creates a PR, the orchestrator atomically chains a follow-up review task with a flat $2 budget and `parent_task_id` pointing at this task |
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
make docker-skill-agent-build-local   # Skill-agent image (claude_code-only; opt-in)
make docker-agents-build-local        # Build agent images
```

Backlite runs agent containers directly against the local Docker daemon; there is no remote orchestration runtime.

The standard agent image lives in `docker/agent/`. `docker/skill-agent/` is a thin claude_code-only image that expresses task behavior as Claude Code skill bundles. Set `BACKFLOW_SKILL_AGENT_IMAGE=<image>` to opt in — claude_code tasks reroute to the skill-agent image; codex tasks continue to use the standard image. Unset to roll back instantly. See [CLAUDE.md](CLAUDE.md#agent-containers) for the full routing rule.

## Configuration

All config via environment variables or `.env` file. See `.env.example` for the full list.

### General

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Required for `claude_code` harness |
| `OPENAI_API_KEY` | Required for `codex` harness |
| `GITHUB_TOKEN` | For cloning private repos and creating PRs |
| `BACKFLOW_LISTEN_ADDR` | Server listen address |
| `BACKFLOW_DATABASE_PATH` | SQLite database path (required) |
| `BACKFLOW_DATA_DIR` | Filesystem root for persisted task artifacts (`container_output.log`, `task.json`) |
| `BACKFLOW_MAX_CONTAINERS` | Concurrency cap (≤ `MaxLocalContainers` in `internal/config/config.go`) |
| `BACKFLOW_POLL_INTERVAL_SEC` | Orchestrator poll interval (seconds) |

See `internal/config/config.go` and `.env.example` for the full surface and current defaults.

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
| `BACKFLOW_SKILL_AGENT_IMAGE` | Optional opt-in: when set, routes every `claude_code` task to a skill-bundle image instead of `BACKFLOW_AGENT_IMAGE`. Codex tasks are unaffected. See [CLAUDE.md](CLAUDE.md#skill-based-agent-image-opt-in). |
| `BACKFLOW_MAX_USER_RETRIES` | Max user-initiated retries per task (see config for default) |
| `BACKFLOW_CONTAINER_CPUS` | CPU cores per container |
| `BACKFLOW_CONTAINER_MEMORY_GB` | Memory (GB) per container |

### Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_WEBHOOK_URL` | | Webhook endpoint URL |
| `BACKFLOW_WEBHOOK_EVENTS` | all | Comma-separated event filter |

Events: `task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`, `task.cancelled`, `task.retry`

### Local SQLite backups

Enabled by default. The server runs a single background worker from the orchestrator tick that takes a consistent online SQLite snapshot, gzip-compresses and verifies it, and writes it into a configurable directory alongside a `.meta.json` sidecar (`file_name`, `created_at`, `finalized_at`, `sha256`, `size_bytes`). Artifacts are named `backlite-YYYYMMDDTHHMMSSZ.sqlite.gz`. The latest artifact's sha256 is recomputed each tick before it is trusted; corrupted artifacts are skipped and the scheduler falls back to the previous valid one.

Each tick also prunes finalized artifacts older than `BACKFLOW_LOCAL_BACKUP_RETENTION_SEC` (with their `.meta.json` and `.upload.json` sidecars), stale temp files, and orphan sidecars. The newest valid artifact is always preserved, regardless of age; setting retention to `0` disables pruning. Operator-visible state lives on `/debug/stats` under the `backup` key: latest-artifact metadata, S3 upload state, worker state, last success/error timestamps, and a ring of recent backup/prune/upload errors. Backup, upload, and retention failures are logged and recorded in that feed but do not affect health checks (`/health`, `/api/v1/health`) or task orchestration.

Optional S3-compatible uploads are enabled by setting `BACKFLOW_BACKUP_S3_BUCKET`. The manager uploads the newest valid local artifact and writes `<artifact>.upload.json` next to the local backup after a successful upload. That marker records bucket, key, endpoint, ETag, size, sha256, and upload time; future ticks validate the marker against the local artifact before suppressing duplicate uploads. Upload failures retry with in-memory backoff and do not force a new local backup.

| Variable | Description |
|----------|-------------|
| `BACKFLOW_LOCAL_BACKUP_ENABLED` | Toggle the local backup worker |
| `BACKFLOW_LOCAL_BACKUP_DIR` | Output directory (supports `~` expansion) |
| `BACKFLOW_LOCAL_BACKUP_INTERVAL_SEC` | Minimum spacing between successful backups |
| `BACKFLOW_LOCAL_BACKUP_RETENTION_SEC` | Age past which finalized backups are pruned (`0` disables pruning) |
| `BACKFLOW_BACKUP_S3_BUCKET` | Enables optional S3-compatible upload to this bucket |
| `BACKFLOW_BACKUP_S3_PREFIX` | Optional object key prefix |
| `BACKFLOW_BACKUP_S3_REGION` | Optional S3 region |
| `BACKFLOW_BACKUP_S3_ENDPOINT` | Optional custom endpoint for S3-compatible providers |
| `BACKFLOW_BACKUP_S3_PATH_STYLE` | Use path-style addressing for compatible providers that require it |

`scripts/setup-backup-bucket.sh` creates or verifies a bucket with AWS CLI-compatible commands. It requires the bucket name via `--bucket` or `BACKFLOW_BACKUP_S3_BUCKET`; encryption and public-access blocking are best-effort because S3-compatible providers vary. Lifecycle retention is only installed when the helper creates a new bucket, scoped to `--prefix` / `BACKFLOW_BACKUP_S3_PREFIX`, so existing bucket lifecycle policies are not overwritten.

For a local end-to-end check of backup uploads against MinIO, run `make test-s3-backup`. The target starts a temporary MinIO container, exercises the real AWS SDK upload path, and removes the container afterward.

Backups cover only the SQLite database at `BACKFLOW_DATABASE_PATH`. Task output files and reading content under `BACKFLOW_DATA_DIR` are not included.

Manual restore:

1. Stop the Backlite server so SQLite is not writing to the database.
2. Choose a local `.sqlite.gz` artifact, or download the corresponding object from S3.
3. Decompress to a separate path: `gunzip -c backlite-...sqlite.gz > restore.sqlite`.
4. Validate the restore candidate: `sqlite3 restore.sqlite "PRAGMA integrity_check;"` should return `ok`.
5. Preserve the current database: `cp "$BACKFLOW_DATABASE_PATH" "$BACKFLOW_DATABASE_PATH.before-restore"`.
6. Replace the configured database path with `restore.sqlite`.
7. Restart Backlite and check `/health` plus `/debug/stats`.
