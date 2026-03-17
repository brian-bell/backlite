# Backflow

Agent orchestrator that runs coding agents (Claude Code or Codex) in ephemeral containers. POST a task (repo + prompt), get back a branch with commits and a PR. Three modes: EC2 spot instances, local Docker, or ECS Fargate.

## Prerequisites

- Go 1.24+
- Docker
- `jq` (for helper scripts)
- AWS CLI (for EC2/Fargate modes)

## Local Development

```bash
cp .env.example .env
# Edit .env — at minimum set ANTHROPIC_API_KEY and GITHUB_TOKEN
# Set BACKFLOW_MODE=local for local Docker (no AWS needed)
```

```bash
make build          # Compile to bin/backflow
make run            # Build + run (auto-sources .env)
make test           # Run all tests (no cache)
make lint           # go vet
make deps           # go mod tidy
make clean          # Remove bin/
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

Tests create temporary SQLite databases — no external services needed.

### Local Tunnel (for SMS/webhooks)

To receive inbound Twilio webhooks during local development, expose your server with [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-apps):

```bash
brew install cloudflared
cloudflared tunnel --url http://localhost:8080
```

cloudflared prints a public URL like `https://random-words.trycloudflare.com`. Set this as the webhook URL in the Twilio Console:

```
https://random-words.trycloudflare.com/webhooks/sms/inbound
```

No account required. The URL changes each time you restart the tunnel. See [docs/sms-setup.md](docs/sms-setup.md) for full SMS configuration.

## Submitting Tasks

PRs are created by default. Use `--no-pr` to skip.

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
  --effort high --self-review

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
# Create a coding task
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "repo_url": "https://github.com/org/repo",
    "prompt": "Fix the bug",
    "create_pr": true,
    "self_review": true
  }'

# Review a PR
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "repo_url": "https://github.com/org/repo",
    "task_mode": "review",
    "review_pr_number": 42
  }'

# Codex harness (requires OPENAI_API_KEY)
curl -X POST http://localhost:8080/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "repo_url": "https://github.com/org/repo",
    "prompt": "Fix the bug",
    "harness": "codex",
    "create_pr": true
  }'
```

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/tasks` | Create a task |
| `GET` | `/api/v1/tasks` | List tasks (`?status=`, `?limit=`, `?offset=`) |
| `GET` | `/api/v1/tasks/{id}` | Get task details |
| `DELETE` | `/api/v1/tasks/{id}` | Cancel a task |
| `GET` | `/api/v1/tasks/{id}/logs` | Container logs (`?tail=100`) |
| `GET` | `/api/v1/health` | Health check |

### Task Request Fields

| Field | Type | Description |
|-------|------|-------------|
| `repo_url` | string | **Required.** Repository URL |
| `prompt` | string | **Required for code mode.** Agent instructions |
| `task_mode` | string | `code` (default) or `review` |
| `harness` | string | `claude_code` (default) or `codex` |
| `model` | string | Model override (default: `claude-sonnet-4-6` / `gpt-5.4` for codex) |
| `effort` | string | `low`, `medium`, `high` (default), or `xhigh` |
| `branch` | string | Working branch name |
| `target_branch` | string | Target branch (default: main) |
| `create_pr` | bool | Create a PR on completion |
| `self_review` | bool | Agent self-reviews the PR after creation |
| `pr_title` | string | Custom PR title |
| `pr_body` | string | Custom PR body |
| `review_pr_number` | int | PR number (required for `review` mode) |
| `max_budget_usd` | float | Budget cap in USD (default: 10) |
| `max_runtime_min` | int | Runtime cap in minutes (default: 30) |
| `max_turns` | int | Max conversation turns (default: 200) |
| `context` | string | Additional context appended to prompt |
| `claude_md` | string | Extra CLAUDE.md content injected into the repo |
| `allowed_tools` | []string | Restrict agent tool access |
| `env_vars` | map | Extra env vars passed to the container |
| `save_agent_output` | bool | Save agent output to S3 (default: true if S3 configured) |

## Monitoring and Operations

```bash
# Database state
make db-status

# Task details
curl -s http://localhost:8080/api/v1/tasks/{id} | jq .

# Container logs
curl -s 'http://localhost:8080/api/v1/tasks/{id}/logs?tail=100'

# Health check
curl -s http://localhost:8080/api/v1/health

# Shell into an agent EC2 instance
aws ssm start-session --target i-0abc...
```

### Task Lifecycle

`pending` -> `provisioning` -> `running` -> `completed` | `failed` | `interrupted` | `cancelled`

Interrupted/failed tasks can enter `recovering` -> re-queued as `pending`.

### Database

SQLite in WAL mode. Auto-migrates on startup. Configured via `BACKFLOW_DB_PATH` (default: `backflow.db`).

```bash
make db-status                              # Dump all tasks and instances
sqlite3 backflow.db ".schema"               # Show schema
sqlite3 backflow.db "SELECT id, status, created_at FROM tasks ORDER BY created_at DESC LIMIT 10;"
```

To reset: delete `backflow.db`. Recreated on next startup.

To add a column: add an idempotent `ALTER TABLE` to `internal/store/sqlite.go:migrate()`, then update the model and queries.

## Deployment

### Agent Image

```bash
make docker-build-local    # Single-arch local build
make docker-deploy         # Multi-arch build + push to ECR
```

### AWS Setup (EC2 Mode)

```bash
make setup-aws
# Creates: ECR repo, IAM role, security group, launch template
# Copy BACKFLOW_LAUNCH_TEMPLATE_ID from output into .env
```

### Fargate Mode

Set `BACKFLOW_MODE=fargate`. No EC2 instances to manage.

```bash
# Required in .env
BACKFLOW_MODE=fargate
BACKFLOW_ECS_CLUSTER=backflow
BACKFLOW_ECS_TASK_DEFINITION=backflow-agent
BACKFLOW_ECS_SUBNETS=subnet-abc123,subnet-def456
BACKFLOW_CLOUDWATCH_LOG_GROUP=/ecs/backflow
```

Prerequisites: ECS cluster with Fargate capacity providers, task definition with `awslogs` log driver, subnets with egress, IAM roles for image pull + log delivery.

`max_subscription` auth is not supported in Fargate mode.

## Configuration

All config via environment variables or `.env` file. See `.env.example` for the full list.

### General

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_MODE` | `ec2` | `ec2`, `local`, or `fargate` |
| `BACKFLOW_AUTH_MODE` | `api_key` | `api_key` or `max_subscription` |
| `ANTHROPIC_API_KEY` | | Required for `api_key` mode |
| `OPENAI_API_KEY` | | Required for `codex` harness |
| `GITHUB_TOKEN` | | For cloning private repos and creating PRs |
| `BACKFLOW_LISTEN_ADDR` | `:8080` | Server listen address |
| `BACKFLOW_DB_PATH` | `backflow.db` | SQLite database path |
| `BACKFLOW_POLL_INTERVAL_SEC` | `5` | Orchestrator poll interval (seconds) |
| `BACKFLOW_S3_BUCKET` | | S3 bucket for agent output and large prompt offload |

### Agent Defaults

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_DEFAULT_HARNESS` | `claude_code` | `claude_code` or `codex` |
| `BACKFLOW_DEFAULT_MODEL` | `claude-sonnet-4-6` | Default model for Claude Code |
| `BACKFLOW_DEFAULT_CODEX_MODEL` | `gpt-5.4` | Default model for Codex |
| `BACKFLOW_DEFAULT_EFFORT` | `high` | Reasoning effort (`low`, `medium`, `high`, `xhigh`) |
| `BACKFLOW_DEFAULT_MAX_BUDGET` | `10` | Budget cap (USD) |
| `BACKFLOW_DEFAULT_MAX_RUNTIME_MIN` | `30` | Runtime cap (minutes) |
| `BACKFLOW_DEFAULT_MAX_TURNS` | `200` | Max conversation turns |
| `BACKFLOW_CONTAINER_CPUS` | `2` | CPU cores per container |
| `BACKFLOW_CONTAINER_MEMORY_GB` | `8` | Memory (GB) per container |

### EC2 Mode

| Variable | Default | Description |
|----------|---------|-------------|
| `AWS_REGION` | `us-east-1` | AWS region |
| `BACKFLOW_INSTANCE_TYPE` | `m7g.xlarge` | EC2 instance type |
| `BACKFLOW_LAUNCH_TEMPLATE_ID` | | From `make setup-aws` |
| `BACKFLOW_MAX_INSTANCES` | `5` | Max EC2 instances |
| `BACKFLOW_CONTAINERS_PER_INSTANCE` | `1` | Containers per instance |

### Fargate Mode

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_ECS_CLUSTER` | | ECS cluster name (required) |
| `BACKFLOW_ECS_TASK_DEFINITION` | | Task definition ARN or family (required) |
| `BACKFLOW_ECS_SUBNETS` | | Comma-separated subnet IDs (required) |
| `BACKFLOW_CLOUDWATCH_LOG_GROUP` | | CloudWatch log group (required) |
| `BACKFLOW_ECS_SECURITY_GROUPS` | | Comma-separated security group IDs |
| `BACKFLOW_ECS_LAUNCH_TYPE` | `FARGATE_SPOT` | `FARGATE` or `FARGATE_SPOT` |
| `BACKFLOW_ECS_CONTAINER_NAME` | `backflow-agent` | Container name in task definition |
| `BACKFLOW_ECS_LOG_STREAM_PREFIX` | `ecs` | CloudWatch log stream prefix |
| `BACKFLOW_ECS_ASSIGN_PUBLIC_IP` | `true` | `false` for private subnets with NAT |
| `BACKFLOW_MAX_CONCURRENT_TASKS` | `5` | Max concurrent Fargate tasks |

### Webhooks

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_WEBHOOK_URL` | | Webhook endpoint URL |
| `BACKFLOW_WEBHOOK_EVENTS` | all | Comma-separated event filter |

Events: `task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`

### Auth Modes

- **`api_key`** (default) -- Anthropic API key, supports concurrent agents
- **`max_subscription`** -- Claude Max credentials via `CLAUDE_CREDENTIALS_PATH`, one agent at a time, not supported in Fargate mode
