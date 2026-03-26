# CLAUDE.md

## What This Is

Backflow is a Go service that runs coding agents (Claude Code or Codex) in ephemeral containers. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

## Commands

```bash
make build              # Build to bin/backflow
make run                # Build + run (sources .env, refreshes AWS creds if needed)
make test               # Unit/integration tests (excludes blackbox; see make test-blackbox)
make lint               # go vet ./...
make test-schema        # Schemathesis fuzz tests against OpenAPI spec (requires docker, goose, schemathesis)
make test-blackbox      # Black-box integration test (builds fake agent, spins up server + DB)
make test-soak          # Soak test (10 min short mode; warns before truncating tasks DB)
make test-fake-agent    # Unit tests for the fake agent Docker image
make docker-fake-agent-build  # Build fake agent image for testing
make deps               # go mod tidy
make clean              # Remove bin/ directory
make cloudflared-setup  # Create cloudflared tunnel, DNS route, and config (one-time)
make tunnel             # Start cloudflared tunnel → $BACKFLOW_DOMAIN → localhost:8080
make db-running         # Show running tasks (also: db-pending, db-completed, db-failed, etc.)
make docker-agent-build       # Buildx multi-platform agent image (amd64+arm64)
make docker-agent-build-local # Single-architecture agent build
make docker-agent-push        # Tag + push agent to ECR (requires REGISTRY=<ecr-uri>)
make docker-agent-deploy      # Full agent ECR pipeline: login, buildx, push
make docker-server-build       # Buildx multi-platform server image (amd64+arm64)
make docker-server-build-local # Single-architecture server build
make docker-server-deploy      # Full server ECR pipeline: login, buildx, push
make deploy-dev         # Deploy current branch to backflow-dev on Fly.io
make setup-aws          # Create AWS infrastructure
make copy-env           # Copy .env from ~/dev/etc/.env to local .env
make overwrite-env      # Copy local .env to ~/dev/etc/.env
goose -dir migrations status # Show pending/applied migrations
goose -dir migrations up     # Apply the next migration(s)
goose -dir migrations down   # Roll back the last migration
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

## Architecture

Two goroutines: chi REST API on `:8080` + polling orchestrator (5s default). Three operating modes: `ec2` (default, spot instances), `local` (Docker on local machine), and `fargate` (one ECS task per Backflow task, no instance management).

**Flow:** Client → API → PostgreSQL → Orchestrator → Docker on EC2 via SSM, local Docker, or ECS/Fargate → Webhooks.

### API endpoints

- `GET /health` — Health check (root-level, always accessible; used by Fly.io)
- `GET /debug/stats` — Operational stats: PID, uptime, running tasks, pool metrics (outside `/api/v1/`, not blocked by `RestrictAPI`)
- `GET /api/v1/health` — Health check (under API prefix; blocked when `BACKFLOW_RESTRICT_API=true`)
- `POST /tasks` — Create task
- `GET /tasks` — List tasks (query params: `status`, `limit`, `offset`)
- `GET /tasks/{id}` — Get task
- `DELETE /tasks/{id}` — Cancel task (sets status to `cancelled`)
- `POST /tasks/{id}/retry` — Retry a failed/interrupted/cancelled task (atomic, gated by `ready_for_retry` and user retry cap)
- `GET /tasks/{id}/logs` — Stream container logs
- `POST /webhooks/discord` — Discord interaction endpoint (signature-verified)
- `POST /webhooks/sms/inbound` — Twilio inbound SMS webhook

### Key modules (`internal/`)

- **api/** — chi router, handlers, JSON responses, `LogFetcher` interface, `NewTask` shared task-creation helper (used by both REST handler and Discord modal), `CancelTask` and `RetryTask` shared action helpers (used by both REST handler and Discord)
- **orchestrator/** — Poll loop (`orchestrator.go`), dispatch (`dispatch.go`), monitoring (`monitor.go`), recovery (`recovery.go`), local mode (`local.go`). Subpackages: `docker/` (Docker container management via SSM or local exec), `ec2/` (EC2 lifecycle, auto-scaler, spot interruption handler), `fargate/` (ECS/Fargate runner, CloudWatch log parsing), `s3/` (agent output upload)
- **store/** — `Store` interface + PostgreSQL (`pgxpool`, goose migrations)
- **models/** — `Task`, `Instance`, `AllowedSender`, and `DiscordInstall` structs with status enums. `FindFirstURL` / `InferReviewMode` auto-detect review mode when a prompt's first URL is a GitHub PR URL.
- **discord/** — Discord interaction handler (Ed25519 signature verification, PING/PONG, interaction routing, `/backflow create` modal for task creation, `/backflow cancel` and `/backflow retry` commands, button click handling). `HandlerActions` struct groups callback functions and role-based authorization config.
- **config/** — Env-var config (`BACKFLOW_*` prefix), three modes (`ec2`/`local`/`fargate`). `RestrictAPI` blocks all `/api/v1/*` endpoints when `BACKFLOW_RESTRICT_API=true` (used in Fly.io deployment). `TaskDefaults(taskMode)` returns resolved defaults; `Apply(task, overrides)` fills zero-value fields using `*bool` overrides (nil = use default, non-nil = use pointed value)
- **notify/** — `Notifier` interface, `WebhookNotifier` (HTTP POST, 3 retries, event filtering), `DiscordNotifier` (lifecycle messages in channel + per-task threads), `NoopNotifier`, `EventBus` (async fan-out delivery via buffered channel), `NewEvent` constructor with `EventOption` functional options, `MessagingNotifier` (SMS via Twilio for reply channels)
- **messaging/** — `Messenger` interface, `TwilioMessenger` (outbound SMS), inbound SMS webhook handler, message parsing
- **debug/** — `/debug/stats` handler: PID, uptime, running task count, pgxpool metrics

### Fake agent (`test/blackbox/fake-agent/`)

Minimal Alpine image used by black-box and soak tests. Reads `FAKE_OUTCOME` env var to simulate outcomes: `success`, `slow_success`, `fail`, `needs_input`, `timeout`, `crash`. Writes `status.json` and emits `BACKFLOW_STATUS_JSON:` just like the real agent. Does not create `claude_output.log` (soak tasks set `save_agent_output: false`).

### Soak test (`test/soak/`)

Long-running resource leak detector. Submits tasks at intervals, collects RSS, pool stats, and container counts, then analyzes for memory growth and container accumulation. Run via `make test-soak` (10-min short mode). Truncates the tasks table and prunes stale containers at start and end. The wrapper script (`scripts/test-soak.sh`) warns before truncating and asks for confirmation.

### Agent container (`docker/agent/`)

Node.js 20 image with Claude Code CLI + Codex CLI + git + gh. `entrypoint.sh`: clone → checkout → inject CLAUDE.md → run agent (with retry up to 3 attempts) → commit → push → create PR → optional self-review. Supports two harnesses: `claude_code` (`--output-format stream-json`, `--max-turns`) and `codex` (`exec --dangerously-bypass-approvals-and-sandbox`). Both harnesses work in code and review modes. Writes `status.json` for Docker-based modes and emits a `BACKFLOW_STATUS_JSON:` line for Fargate log parsing.

### Statuses

- **Task:** `pending` → `provisioning` → `running` → `completed` | `failed` | `interrupted` | `cancelled` | `recovering` → `pending` | `running` | `completed` | `failed`
- **Instance:** `pending` → `running` → `draining` → `terminated`

### Webhook events

`task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`, `task.cancelled`, `task.retry`

### Discord integration

When `BACKFLOW_DISCORD_APP_ID` is set, Backflow enables the Discord integration:

Required env vars:

- `BACKFLOW_DISCORD_APP_ID` — Discord application ID
- `BACKFLOW_DISCORD_PUBLIC_KEY` — Ed25519 public key for interaction verification
- `BACKFLOW_DISCORD_BOT_TOKEN` — Bot token for API calls
- `BACKFLOW_DISCORD_GUILD_ID` — Target server ID
- `BACKFLOW_DISCORD_CHANNEL_ID` — Target channel ID

Optional env vars:

- `BACKFLOW_DISCORD_ALLOWED_ROLES` (comma-separated role IDs for mutation authorization)
- `BACKFLOW_DISCORD_EVENTS` (comma-separated event filter; nil = all events)

At startup, Backflow persists the install config to the `discord_installs` table, registers the `/backflow` slash command (with `create`, `status`, `list`, `cancel`, and `retry` subcommands) via the Discord API, mounts the interaction handler at `/webhooks/discord`, and subscribes a `DiscordNotifier` to the event bus. The `/backflow create` subcommand opens a modal dialog for task creation. The `/backflow cancel <task_id>` and `/backflow retry <task_id>` subcommands cancel or retry tasks respectively, with role-based permission enforcement via `BACKFLOW_DISCORD_ALLOWED_ROLES`. The notifier creates a channel message on the first event for each task, then posts subsequent events as replies in a per-task thread. Thread messages include Cancel buttons for active tasks and Retry buttons for failed/interrupted tasks (cancelled tasks show a Retry button only after container cleanup completes).

### Slack notification stub

- `BACKFLOW_SLACK_WEBHOOK_URL`
- `BACKFLOW_SLACK_EVENTS` (comma-separated event filter)

If the Slack webhook URL is set, `cmd/backflow/main.go` logs that the subscriber is not yet implemented.

## Harnesses

- **`claude_code`** — Claude Code CLI. Requires `ANTHROPIC_API_KEY` or Max subscription credentials.
- **`codex`** — OpenAI Codex CLI. Requires `OPENAI_API_KEY`.

Configured per-task via the `harness` field or globally via `BACKFLOW_DEFAULT_HARNESS`.

PR comments include actual cost for `claude_code` (extracted from `total_cost_usd` in stream-json output). Codex CLI doesn't report cost in dollars — only raw token counts via `--json` — so cost is omitted for `codex` harness runs.

## Auth modes

- **`api_key`** — Anthropic API key via `ANTHROPIC_API_KEY`, concurrent agents (max_instances × containers_per_instance)
- **`max_subscription`** — Claude Max credentials via `CLAUDE_CREDENTIALS_PATH` volume mount, serial (one agent at a time)

`max_subscription` is not supported in `fargate` mode. Initial Fargate support assumes API-key auth only.

## Fargate mode

Set `BACKFLOW_MODE=fargate` to run each Backflow task as a standalone ECS task. Capacity is tracked through a synthetic `fargate` instance in PostgreSQL; there are no EC2 instances to launch or drain.

Required env vars:

- `BACKFLOW_ECS_CLUSTER`
- `BACKFLOW_ECS_TASK_DEFINITION`
- `BACKFLOW_ECS_SUBNETS` (comma-separated)
- `BACKFLOW_CLOUDWATCH_LOG_GROUP`

Optional Fargate env vars:

- `BACKFLOW_ECS_SECURITY_GROUPS` (comma-separated)
- `BACKFLOW_ECS_LAUNCH_TYPE` (`FARGATE` or `FARGATE_SPOT`, default `FARGATE_SPOT`)
- `BACKFLOW_ECS_CONTAINER_NAME` (default `backflow-agent`)
- `BACKFLOW_ECS_LOG_STREAM_PREFIX` (default `ecs`)
- `BACKFLOW_ECS_ASSIGN_PUBLIC_IP` (`true` or `false`, default `true`; set to `false` for private subnets with NAT)
- `BACKFLOW_MAX_CONCURRENT_TASKS` (default `5`)

ECS prerequisites:

- ECS cluster with Fargate enabled and, if using `FARGATE_SPOT`, Fargate capacity providers associated to the cluster
- Task definition whose main container name matches `BACKFLOW_ECS_CONTAINER_NAME`
- Task definition configured with the `awslogs` log driver writing into `BACKFLOW_CLOUDWATCH_LOG_GROUP`
- Subnets and security groups in the same VPC, with egress for git/GitHub/API traffic
- IAM execution/task roles allowing image pull, log delivery, and whatever repository/API access the agent needs

## Fly.io deployment

Two Fly apps: production (`backflow`, `fly.toml`) and dev (`backflow-dev`, `fly.dev.toml`).

**Production** auto-deploys on push to main via `.github/workflows/ci.yml`. `BACKFLOW_RESTRICT_API=true` blocks all `/api/v1/*` endpoints; webhook paths and `/health` are unaffected.

**Dev** is deployed on demand via `make deploy-dev` (local) or the `Deploy Dev` workflow dispatch (GitHub Actions, takes a `ref` input). The dev machine auto-stops when idle (`auto_stop_machines = "stop"`, `min_machines_running = 0`).

AWS credentials for ECS/S3/CloudWatch are provided via the `backflow-fly` IAM user (created by `make setup-aws`). See `docs/fly-setup.md` for deployment steps.

## Documentation guidelines

Do not record default values for config or env vars in documentation. Defaults change frequently and docs drift silently. Instead, point to the source (`internal/config/config.go`) or say "see config for current defaults."

## Design patterns

- Interface abstractions (`Store`, `Notifier`, `LogFetcher`) for testability
- Polling over events for simplicity
- SSM instead of SSH (no key management) in EC2 mode; direct Docker exec in local mode
- ULID task IDs with `bf_` prefix
- Zerolog structured logging
- Spot interruption detection with automatic task re-queuing

## Database

PostgreSQL via Supabase (session pooler). Migrations are managed by [goose](https://github.com/pressly/goose) and live in `migrations/`. The store implementation is in `internal/store/postgres.go` using `pgxpool`. Set `BACKFLOW_DATABASE_URL` to the Supabase session pooler connection string.

Migration workflow:

```bash
goose -dir migrations status
goose -dir migrations up
goose -dir migrations down
```

Create new migrations in `migrations/` with the next numeric prefix, `-- +goose Up`, and `-- +goose Down`.

## Documentation

Additional docs in `docs/`:
- `ROADMAP.md` — Product roadmap with phased implementation plan
- `schema.md` — Database schema (tables, columns, indexes, status lifecycles)
- `discord-setup.md` — Discord bot creation, server install, and Backflow configuration
- `sms-setup.md` — Twilio SMS setup and allowed sender configuration
- `sizing.md` — EC2 instance sizing and container density guide
- `setup-ci.md` — GitHub Actions CI/CD setup for agent image builds
- `fly-setup.md` — Fly.io deployment setup and configuration
