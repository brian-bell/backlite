# Backflow

Background agent orchestrator that spins up ephemeral Docker containers on AWS EC2 spot instances to run Claude Code against coding tasks.

## How It Works

1. You POST a task (repo URL + prompt) to the REST API
2. The orchestrator provisions an EC2 spot instance (if needed)
3. A Docker container clones the repo, runs Claude Code, commits, pushes, and optionally creates a PR
4. Webhook notifications fire on lifecycle events (completed, failed, needs input)

## Auth Modes

- **`api_key`** (default) — Uses an Anthropic API key. Supports multiple concurrent agents. Token costs apply.
- **`max_subscription`** — Uses a Claude Max subscription. Strictly one agent at a time. Flat monthly fee.

## Setup

### Prerequisites

- Go 1.24+
- Docker
- AWS CLI (authenticated)
- An AWS account with EC2, ECR, SSM, and IAM access

### 1. AWS Infrastructure

```bash
make setup-aws
```

Creates: ECR repo, IAM role (EC2 + SSM + ECR), security group (outbound-only), launch template with latest Amazon Linux 2023 AMI.

Note the `BACKFLOW_LAUNCH_TEMPLATE_ID` in the output.

### 2. Build & Push Agent Image

```bash
make docker-deploy
# or if docker requires sudo:
make docker-deploy DOCKER="sudo docker"
```

### 3. Configure

```bash
cp .env.example .env
```

Edit `.env` with your values:

```
BACKFLOW_AUTH_MODE=api_key
ANTHROPIC_API_KEY=sk-ant-...
GITHUB_TOKEN=ghp_...
AWS_REGION=us-east-1
BACKFLOW_LAUNCH_TEMPLATE_ID=lt-...
```

### 4. Run

```bash
make run
```

Server starts on `:8080` by default.

## Usage

### Create a Task

```bash
./scripts/create-task.sh https://github.com/org/repo "Fix the login bug" --pr
```

Or with more options:

```bash
./scripts/create-task.sh https://github.com/org/repo "Add unit tests" \
  --pr --pr-title "Add tests" \
  --budget 15 --model claude-sonnet-4-6 \
  --context "Focus on the auth module"
```

### API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/tasks` | Create a task |
| GET | `/api/v1/tasks` | List tasks (filters: `?status=`, `?limit=`, `?offset=`) |
| GET | `/api/v1/tasks/{id}` | Get a task |
| DELETE | `/api/v1/tasks/{id}` | Cancel/delete a task |
| GET | `/api/v1/tasks/{id}/logs` | Get agent container logs (`?tail=100`) |
| GET | `/api/v1/health` | Health check |

### Check Status

```bash
# DB dump
make db-status

# Task logs
curl http://localhost:8080/api/v1/tasks/bf_01KK.../logs

# SSH into agent VM (no SSH keys needed)
aws ssm start-session --target i-0abc...
```

## Architecture

```
┌─────────┐     ┌──────────────────────────────────────┐
│  Client  │────▶│  Backflow Server (:8080)              │
└─────────┘     │  ├── REST API (chi)                   │
                │  ├── Orchestrator (5s poll loop)       │
                │  │   ├── Scaler (EC2 spot instances)   │
                │  │   ├── Docker (containers via SSM)   │
                │  │   └── Spot handler (re-queue)       │
                │  ├── SQLite store (WAL mode)           │
                │  └── Webhook notifier                  │
                └──────────────┬───────────────────────┘
                               │ SSM
                ┌──────────────▼───────────────────────┐
                │  EC2 Spot Instance (t4g.medium)       │
                │  ├── Docker container: backflow-agent │
                │  │   ├── Clone repo                   │
                │  │   ├── Run Claude Code              │
                │  │   ├── Commit + push                │
                │  │   └── Create PR                    │
                │  └── (up to 4 containers per instance)│
                └──────────────────────────────────────┘
```

## Instance Lifecycle

1. Task submitted → orchestrator checks for capacity
2. No capacity → scaler launches a spot instance (saved as `pending`)
3. Scaler polls EC2 until instance is running, SSM is online, and Docker + agent image are ready → marked `running`
4. Orchestrator dispatches task → runs container via SSM
5. Container finishes → orchestrator captures status, fires webhook
6. Instance idle for 5 minutes → scaler terminates it

Spot interruptions: instances are re-checked, tasks on interrupted instances are re-queued as `pending` with incremented retry count.

## Webhooks

Set `BACKFLOW_WEBHOOK_URL` in `.env`. Receives POST with:

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

## Config Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKFLOW_AUTH_MODE` | `api_key` | `api_key` or `max_subscription` |
| `ANTHROPIC_API_KEY` | | Required for `api_key` mode |
| `CLAUDE_CREDENTIALS_PATH` | | Path to `~/.claude/` for `max_subscription` mode |
| `GITHUB_TOKEN` | | For cloning private repos and creating PRs |
| `BACKFLOW_LISTEN_ADDR` | `:8080` | Server listen address |
| `BACKFLOW_DB_PATH` | `backflow.db` | SQLite database path |
| `AWS_REGION` | `us-east-1` | AWS region |
| `BACKFLOW_INSTANCE_TYPE` | `t4g.medium` | EC2 instance type |
| `BACKFLOW_LAUNCH_TEMPLATE_ID` | | Launch template from `setup-aws` |
| `BACKFLOW_MAX_INSTANCES` | `5` | Max EC2 instances |
| `BACKFLOW_CONTAINERS_PER_INSTANCE` | `4` | Max containers per instance |
| `BACKFLOW_DEFAULT_MODEL` | `claude-sonnet-4-6` | Default Claude model |
| `BACKFLOW_DEFAULT_MAX_BUDGET` | `10` | Default max budget (USD) |
| `BACKFLOW_DEFAULT_MAX_RUNTIME_MIN` | `30` | Default max runtime (minutes) |
| `BACKFLOW_DEFAULT_MAX_TURNS` | `200` | Default max conversation turns |
| `BACKFLOW_WEBHOOK_URL` | | Webhook endpoint |
| `BACKFLOW_WEBHOOK_EVENTS` | all | Comma-separated event filter |
| `BACKFLOW_POLL_INTERVAL_SEC` | `5` | Orchestrator poll interval |
| `DOCKER` | `docker` | Docker command (set to `sudo docker` if needed) |

## Development

```bash
make build          # Build server binary
make run            # Build + run (sources .env)
make test           # Run tests
make lint           # go vet
make db-status      # Dump database state
make docker-deploy  # Build + push agent image to ECR
make setup-aws      # Create AWS infrastructure
```
