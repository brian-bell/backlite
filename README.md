# Backflow

Background agent orchestrator that runs Claude Code in ephemeral Docker containers on AWS EC2 spot instances. POST a task (repo + prompt), get back a branch with commits and optionally a PR.

## Prerequisites

- Go 1.24+
- AWS CLI (configured with credentials)
- Docker
- `sqlite3` CLI (for `make db-status`)

## Local Development

### Setup

```bash
cp .env.example .env
# Edit .env — at minimum set ANTHROPIC_API_KEY and GITHUB_TOKEN
```

For local-only development without AWS, set `BACKFLOW_MODE=local` in `.env`. This skips EC2 provisioning and runs containers on the local Docker daemon.

### Build and Run

```bash
make build          # Compile to bin/backflow
make run            # Build + run (auto-sources .env)
```

Server starts on `http://localhost:8080`. The orchestrator poll loop and HTTP server run as concurrent goroutines.

### Testing

```bash
make test           # Run all tests (no cache)
make lint           # go vet

# Run a single test
go test ./internal/store/ -run TestCreateTask -v

# Run tests for a specific package
go test ./internal/api/ -v -count=1
```

Tests create temporary SQLite databases that are cleaned up automatically. No external services needed.

### Build the Agent Image Locally

```bash
make docker-build-local    # Single-arch build, no push
```

## Database

Backflow uses SQLite in WAL mode. The database file location is configured via `BACKFLOW_DB_PATH` (default: `backflow.db` in the working directory).

### Schema

Two tables: `tasks` and `instances`. See `internal/store/sqlite.go` for the full schema.

### Migrations

There are no separate migration files. Schema is managed in `internal/store/sqlite.go` in the `migrate()` method. All DDL uses `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS`, so it's safe to run on every startup.

**To add a new column:**

1. Add an `ALTER TABLE ... ADD COLUMN` statement to `migrate()` in `internal/store/sqlite.go`, wrapped in an idempotent check (SQLite will error if the column already exists, so ignore the error or check `pragma table_info` first).
2. Update the `INSERT`, `UPDATE`, and `SELECT` statements in the same file.
3. Update the model struct in `internal/models/`.

**To inspect the database:**

```bash
make db-status                              # Dump all tasks and instances
sqlite3 backflow.db ".schema"               # Show schema
sqlite3 backflow.db "SELECT id, status, created_at FROM tasks ORDER BY created_at DESC LIMIT 10;"
```

**To reset the database:** delete the `backflow.db` file. It will be recreated on next startup.

## AWS Setup (EC2 Mode)

### One-Time Infrastructure

```bash
make setup-aws
```

This creates: ECR repo, IAM role, security group, and launch template. Copy the `BACKFLOW_LAUNCH_TEMPLATE_ID` from the output into `.env`.

### Deploy Agent Image

```bash
make docker-deploy
# If docker needs sudo: make docker-deploy DOCKER="sudo docker"
```

Builds a multi-arch image (amd64 + arm64) and pushes to ECR.

## Submitting Tasks

```bash
# Simple task
./scripts/create-task.sh https://github.com/org/repo "Fix the login bug"

# With PR creation
./scripts/create-task.sh https://github.com/org/repo "Fix the login bug" --pr

# Full options
./scripts/create-task.sh https://github.com/org/repo "Add unit tests" \
  --pr --pr-title "Add tests" \
  --budget 15 --model claude-sonnet-4-6 \
  --branch my-feature --target-branch develop \
  --context "Focus on the auth module" \
  --claude-md "Always use table-driven tests" \
  --env "GOPRIVATE=github.com/org/*"
```

Or call the API directly:

```bash
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{"repo_url": "https://github.com/org/repo", "prompt": "Fix the bug", "create_pr": true}'
```

## Monitoring and Operations

```bash
# Database state
make db-status

# Task details
curl http://localhost:8080/api/v1/tasks/{id}

# Container logs
curl http://localhost:8080/api/v1/tasks/{id}/logs?tail=100

# Health check
curl http://localhost:8080/api/v1/health

# Shell into an agent EC2 instance
aws ssm start-session --target i-0abc...
```

### Task Lifecycle

`pending` → `provisioning` → `running` → `completed` | `failed` | `interrupted` | `cancelled`

### Instance Lifecycle

`pending` → `running` → idle 5 min → `terminated`. Spot interruptions automatically re-queue affected tasks.

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/tasks` | Create a task |
| `GET` | `/api/v1/tasks` | List tasks (`?status=`, `?limit=`, `?offset=`) |
| `GET` | `/api/v1/tasks/{id}` | Get task details |
| `DELETE` | `/api/v1/tasks/{id}` | Cancel a task |
| `GET` | `/api/v1/tasks/{id}/logs` | Container logs (`?tail=100`) |
| `GET` | `/api/v1/health` | Health check |

## Auth Modes

- **`api_key`** (default) — Uses Anthropic API key. Supports multiple concurrent agents. Pay per token.
- **`max_subscription`** — Uses Claude Max subscription credentials. One agent at a time. Flat rate.

## Webhooks

Set `BACKFLOW_WEBHOOK_URL` in `.env`:

```json
{
  "event": "task.completed",
  "task_id": "bf_01KK...",
  "repo_url": "https://github.com/org/repo",
  "prompt": "Fix the bug",
  "message": "",
  "agent_log_tail": "last 20 lines...",
  "timestamp": "2026-03-13T22:00:00Z"
}
```

Events: `task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`

Filter with `BACKFLOW_WEBHOOK_EVENTS=task.completed,task.failed`.

## Configuration

All config is via environment variables (or `.env` file).

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_MODE` | `ec2` | `ec2` or `local` |
| `BACKFLOW_AUTH_MODE` | `api_key` | `api_key` or `max_subscription` |
| `ANTHROPIC_API_KEY` | | Required for `api_key` mode |
| `CLAUDE_CREDENTIALS_PATH` | | Path to `~/.claude/` for `max_subscription` mode |
| `GITHUB_TOKEN` | | For cloning private repos and creating PRs |
| `BACKFLOW_LISTEN_ADDR` | `:8080` | Server listen address |
| `BACKFLOW_DB_PATH` | `backflow.db` | SQLite database path |
| `AWS_REGION` | `us-east-1` | AWS region |
| `BACKFLOW_INSTANCE_TYPE` | `t4g.medium` | EC2 instance type |
| `BACKFLOW_LAUNCH_TEMPLATE_ID` | | From `make setup-aws` |
| `BACKFLOW_MAX_INSTANCES` | `5` | Max EC2 instances |
| `BACKFLOW_CONTAINERS_PER_INSTANCE` | `4` | Containers per instance |
| `BACKFLOW_DEFAULT_MODEL` | `claude-sonnet-4-6` | Default Claude model |
| `BACKFLOW_DEFAULT_MAX_BUDGET` | `10` | Default budget (USD) |
| `BACKFLOW_DEFAULT_MAX_RUNTIME_MIN` | `30` | Default max runtime (min) |
| `BACKFLOW_DEFAULT_MAX_TURNS` | `200` | Default max turns |
| `BACKFLOW_WEBHOOK_URL` | | Webhook endpoint |
| `BACKFLOW_WEBHOOK_EVENTS` | all | Comma-separated event filter |
| `BACKFLOW_POLL_INTERVAL_SEC` | `5` | Orchestrator poll interval (sec) |
| `DOCKER` | `docker` | Docker command (e.g. `sudo docker`) |
