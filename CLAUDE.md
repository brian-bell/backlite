# CLAUDE.md

## What This Is

Backflow is a Go service that runs coding agents (Claude Code or Codex) in ephemeral containers. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

## Commands

```bash
make build              # Build to bin/backflow
make run                # Build + run (sources .env, refreshes AWS creds if needed)
make test               # go test ./... -v -count=1
make lint               # go vet ./...
make deps               # go mod tidy
make clean              # Remove bin/ directory
make cloudflared-setup  # Create cloudflared tunnel, DNS route, and config (one-time)
make tunnel             # Start cloudflared tunnel → $BACKFLOW_DOMAIN → localhost:8080
make db-running         # Show running tasks (also: db-pending, db-completed, db-failed, etc.)
make docker-build       # Buildx multi-platform (amd64+arm64) image
make docker-build-local # Single-architecture build
make docker-push        # Tag + push to ECR (requires REGISTRY=<ecr-uri>)
make docker-deploy      # Full ECR pipeline: login, buildx, push
make setup-aws          # Create AWS infrastructure
goose -dir migrations status # Show pending/applied migrations
goose -dir migrations up     # Apply the next migration(s)
goose -dir migrations down   # Roll back the last migration
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

## Architecture

Two goroutines: chi REST API on `:8080` + polling orchestrator (5s default). Three operating modes: `ec2` (default, spot instances), `local` (Docker on local machine), and `fargate` (one ECS task per Backflow task, no instance management).

**Flow:** Client → API → PostgreSQL → Orchestrator → Docker on EC2 via SSM, local Docker, or ECS/Fargate → Webhooks.

### API endpoints (`/api/v1`)

- `GET /health` — Health check
- `POST /tasks` — Create task
- `GET /tasks` — List tasks (query params: `status`, `limit`, `offset`)
- `GET /tasks/{id}` — Get task
- `DELETE /tasks/{id}` — Cancel task (sets status to `cancelled`)
- `GET /tasks/{id}/logs` — Stream container logs
- `POST /webhooks/discord` — Discord interaction endpoint (signature-verified)
- `POST /webhooks/sms/inbound` — Twilio inbound SMS webhook

### Key modules (`internal/`)

- **api/** — chi router, handlers, JSON responses, `LogFetcher` interface
- **orchestrator/** — Poll loop (`orchestrator.go`), dispatch (`dispatch.go`), monitoring (`monitor.go`), recovery (`recovery.go`), local mode (`local.go`). Subpackages: `docker/` (Docker container management via SSM or local exec), `ec2/` (EC2 lifecycle, auto-scaler, spot interruption handler), `fargate/` (ECS/Fargate runner, CloudWatch log parsing), `s3/` (agent output upload)
- **store/** — `Store` interface + PostgreSQL (`pgxpool`, goose migrations)
- **models/** — `Task`, `Instance`, `AllowedSender`, and `DiscordInstall` structs with status enums
- **discord/** — Discord interaction handler (Ed25519 signature verification, PING/PONG, interaction routing)
- **config/** — Env-var config (`BACKFLOW_*` prefix), three modes (`ec2`/`local`/`fargate`)
- **notify/** — `Notifier` interface, `WebhookNotifier` (HTTP POST, 3 retries, event filtering), `DiscordNotifier` (stub, event filtering, logs only), `NoopNotifier`, `EventBus` (async fan-out delivery via buffered channel), `NewEvent` constructor with `EventOption` functional options, `MessagingNotifier` (SMS via Twilio for reply channels)
- **messaging/** — `Messenger` interface, `TwilioMessenger` (outbound SMS), inbound SMS webhook handler, message parsing

### Agent container (`docker/`)

Node.js 20 image with Claude Code CLI + git + gh. `entrypoint.sh`: clone → checkout → inject CLAUDE.md → run agent (with retry up to 3 attempts) → commit → push → create PR → optional self-review. Supports two harnesses: `claude_code` (default, `--output-format stream-json`) and `codex` (`--full-auto --quiet`). Writes `status.json` for Docker-based modes and emits a `BACKFLOW_STATUS_JSON:` line for Fargate log parsing.

### Statuses

- **Task:** `pending` → `provisioning` → `running` → `completed` | `failed` | `interrupted` | `cancelled` | `recovering` → `pending` | `running` | `completed` | `failed`
- **Instance:** `pending` → `running` → `draining` → `terminated`

### Webhook events

`task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`

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

At startup, Backflow persists the install config to the `discord_installs` table, registers the `/backflow` slash command via the Discord API, mounts the interaction handler at `/webhooks/discord`, and subscribes a `DiscordNotifier` stub to the event bus. Actual Discord message delivery will be implemented in a future issue.

### Slack notification stub

- `BACKFLOW_SLACK_WEBHOOK_URL`
- `BACKFLOW_SLACK_EVENTS` (comma-separated event filter)

If the Slack webhook URL is set, `cmd/backflow/main.go` logs that the subscriber is not yet implemented.

## Harnesses

- **`claude_code`** — Claude Code CLI. Requires `ANTHROPIC_API_KEY` or Max subscription credentials.
- **`codex`** (default) — OpenAI Codex CLI. Requires `OPENAI_API_KEY`. Defaults to `gpt-5.4-mini` model.

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
- `schema.md` — Database schema (tables, columns, indexes, status lifecycles)
- `discord-setup.md` — Discord bot creation, server install, and Backflow configuration
- `sms-setup.md` — Twilio SMS setup and allowed sender configuration
- `sizing.md` — EC2 instance sizing and container density guide
- `setup-ci.md` — GitHub Actions CI/CD setup for agent image builds
