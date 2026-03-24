# Backflow

Agent orchestrator that runs coding agents (Claude Code or Codex) in ephemeral containers. POST a task (repo + prompt), get back a branch with commits and a PR. Three modes: EC2 spot instances, local Docker, or ECS Fargate.

## Prerequisites

- Go 1.25+
- Docker
- PostgreSQL (or Supabase)
- `jq` (for helper scripts)
- AWS CLI (for EC2/Fargate modes)

## Local Development

```bash
cp .env.example .env
# Edit .env — at minimum set BACKFLOW_DATABASE_URL, ANTHROPIC_API_KEY, and GITHUB_TOKEN
# Set BACKFLOW_MODE=local for local Docker (no AWS needed)
```

```bash
make build          # Compile to bin/backflow
make run            # Build + run (auto-sources .env, refreshes AWS creds if needed)
make test           # Run all tests (no cache)
make lint           # go vet
make tunnel         # Start cloudflared tunnel → $BACKFLOW_DOMAIN → localhost:8080
make deps           # go mod tidy
make clean          # Remove bin/
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

Tests use [testcontainers](https://testcontainers.com/) to spin up ephemeral PostgreSQL instances — Docker must be running.

### Local Tunnel (for webhooks)

To receive inbound webhooks (Discord interactions, Twilio SMS) during local development, expose your server with a [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-apps) named tunnel:

```bash
brew install cloudflared
# Set BACKFLOW_TUNNEL_NAME and BACKFLOW_DOMAIN in .env
make cloudflared-setup   # One-time: create tunnel, DNS route, and config
make tunnel              # Start the tunnel
```

This routes `https://$BACKFLOW_DOMAIN` to `localhost:8080`. Set the domain as the Discord Interactions Endpoint URL and Twilio webhook URL. Discord task lifecycle notifications will then land in the configured channel and per-task thread. See [docs/discord-setup.md](docs/discord-setup.md) and [docs/sms-setup.md](docs/sms-setup.md) for full configuration.

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
| `GET` | `/api/v1/tasks/{id}/logs` | Container logs (`?tail=100`) |
| `GET` | `/api/v1/health` | Health check |
| `POST` | `/webhooks/discord` | Discord interaction endpoint (signature-verified) |
| `POST` | `/webhooks/sms/inbound` | Twilio inbound SMS webhook |

### Task Request Fields

| Field | Type | Description |
|-------|------|-------------|
| `repo_url` | string | **Required.** Repository URL |
| `prompt` | string | **Required for code mode.** Agent instructions |
| `task_mode` | string | `code` or `review`; auto-detected from PR URLs in prompt when unset |
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
| `env_vars` | map | Extra env vars passed to the container |
| `save_agent_output` | bool | Save agent output to S3 (omit to use server default) |

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

# Shell into an agent EC2 instance
aws ssm start-session --target i-0abc...
```

### Task Lifecycle

`pending` -> `provisioning` -> `running` -> `completed` | `failed` | `interrupted` | `cancelled`

Interrupted/failed tasks can enter `recovering` -> re-queued as `pending`.

### Database

PostgreSQL (hosted on Supabase, connected via session pooler). Migrations managed by [goose](https://github.com/pressly/goose) in `migrations/`. Auto-runs on startup. Configured via `BACKFLOW_DATABASE_URL`.

```bash
make db-running                             # Show running tasks
make db-pending                             # Show pending tasks
make db-completed                           # Show completed tasks
make db-failed                              # Show failed tasks
psql "$BACKFLOW_DATABASE_URL" -c "\dt"      # List tables
psql "$BACKFLOW_DATABASE_URL" -c "SELECT id, status, created_at FROM tasks ORDER BY created_at DESC LIMIT 10;"
```

To add a migration: create a new file in `migrations/` (e.g. `002_add_column.sql`) with `-- +goose Up` and `-- +goose Down` sections.

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

## Code Review Agents

This project includes Claude Code agent definitions (`.claude/agents/`) for automated code review.

### Full team review

```bash
claude --agent review-lead
```

Spawns a coordinated team of 4 specialized reviewers (structure, errors, style, security) that examine all non-test Go files and produce a prioritized cleanup report.

### Individual reviewers

Run a single reviewer for a focused analysis:

```bash
claude --agent structure-reviewer   # Architecture, duplication, dead code
claude --agent error-reviewer       # Error handling, resource management
claude --agent style-reviewer       # Go idioms, naming, simplification
claude --agent security-reviewer    # Injection, auth, secrets, input validation
```

All agents are read-only — they suggest changes but never modify files. Test files are excluded from review.

## Feature Acceptance Agents

A separate agent team evaluates features from a product perspective — not code quality, but whether a feature is complete, safe, maintainable, and documented. All reviewers evaluate against `docs/ROADMAP.md` as the north star for product direction.

### Review a PR

```bash
claude --agent acceptance-lead "Review PR #42"
```

### Review an existing feature

```bash
claude --agent acceptance-lead "Review the discord feature"
claude --agent acceptance-lead "Review the fargate feature"
claude --agent acceptance-lead "Review the notifications feature"
```

The lead spawns 5 specialist reviewers in parallel:

| Reviewer | Focus |
|----------|-------|
| `product-reviewer` | Roadmap alignment, persona fit, competitive positioning, cross-mode completeness |
| `acceptance-security-reviewer` | Attack surface, auth boundaries, credential handling, threat model |
| `quality-reviewer` | Test coverage, edge cases, status transitions, graceful degradation |
| `maintainability-reviewer` | Pattern consistency, config model, observability, complexity budget |
| `documentation-reviewer` | CLAUDE.md accuracy, config docs, schema docs, discoverability |

The consolidated report ends with a verdict: **ACCEPT**, **ACCEPT WITH CONDITIONS**, or **REQUEST CHANGES**.

All agents are read-only — they analyze and report but never modify files or post PR comments.

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
| `BACKFLOW_DATABASE_URL` | | PostgreSQL connection string (Supabase session pooler recommended) |
| `BACKFLOW_POLL_INTERVAL_SEC` | `5` | Orchestrator poll interval (seconds) |
| `BACKFLOW_S3_BUCKET` | | S3 bucket for agent output and large prompt offload |

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
| `BACKFLOW_CONTAINER_CPUS` | CPU cores per container |
| `BACKFLOW_CONTAINER_MEMORY_GB` | Memory (GB) per container |

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

Events: `task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`, `task.cancelled`

### Discord

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_DISCORD_APP_ID` | | Discord application ID (enables integration when set) |
| `BACKFLOW_DISCORD_PUBLIC_KEY` | | Ed25519 public key for interaction verification |
| `BACKFLOW_DISCORD_BOT_TOKEN` | | Bot token for API calls |
| `BACKFLOW_DISCORD_GUILD_ID` | | Target server ID |
| `BACKFLOW_DISCORD_CHANNEL_ID` | | Target channel ID |
| `BACKFLOW_DISCORD_ALLOWED_ROLES` | | Comma-separated role IDs for mutation authorization |
| `BACKFLOW_DISCORD_EVENTS` | all | Comma-separated event filter |

See [docs/discord-setup.md](docs/discord-setup.md) for full setup instructions.

> **Known issue:** Task retry via Discord (button and `/backflow retry`) is broken — clicking Retry immediately after Cancel requeues the task before the old container is stopped, so the old container runs to completion instead of a new one starting.

### SMS (Twilio)

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_SMS_PROVIDER` | | Set to `twilio` to enable SMS |
| `TWILIO_ACCOUNT_SID` | | Twilio Account SID (required when provider is `twilio`) |
| `TWILIO_AUTH_TOKEN` | | Twilio Auth Token (required when provider is `twilio`) |
| `BACKFLOW_SMS_FROM_NUMBER` | | Twilio phone number in E.164 format (required when provider is `twilio`) |
| `BACKFLOW_SMS_EVENTS` | `task.completed,task.failed` | Comma-separated events that trigger outbound SMS |
| `BACKFLOW_SMS_OUTBOUND_ENABLED` | `true` | Set to `false` to disable outbound SMS while keeping inbound |

See [docs/sms-setup.md](docs/sms-setup.md) for full setup instructions including allowed sender registration and A2P 10DLC compliance.

### Auth Modes

- **`api_key`** (default) -- Anthropic API key, supports concurrent agents
- **`max_subscription`** -- Claude Max credentials via `CLAUDE_CREDENTIALS_PATH`, one agent at a time, not supported in Fargate mode
