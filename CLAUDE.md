# CLAUDE.md

## What This Is

Backflow is a Go service that runs coding agents (Claude Code or Codex) in ephemeral containers. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

Three task modes: `code` (default: clone → code → commit → PR), `review` (PR review with inline comments), and `read` (fetch a URL, summarize it via a reader agent, embed the TL;DR, store the result in the `readings` table for later similarity search).

## HANDOFF.md

`HANDOFF.md` at the repo root captures cross-PR tradeoffs and decisions that aren't obvious from the diff alone — what was deferred, what was unblocked for future issues, and why an alternative was rejected.

- **Before writing a plan:** read `HANDOFF.md` and weigh any notes that apply to the current task. Prior decisions may constrain or inform the approach.
- **When writing a plan:** add brief notes to `HANDOFF.md` about explicit tradeoffs decided for this change — especially any decisions the user expressed directly (e.g. "add Force now vs defer to #175"). Record the decision, the alternatives considered, and the consequence for downstream work. Keep each entry tight; this file is a ledger, not a design doc. Items should be limited to forward-looking constraints or explicit deferrals that a subsequent issue will need to know about.

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
make test-reader-scripts         # Reader-agent shell script tests
make test-reader-status-writer   # Reader-agent status writer test
make test-docker-status-writer   # Agent-container status writer test
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
make docker-reader-build       # Buildx multi-platform reader image (amd64+arm64)
make docker-reader-build-local # Single-architecture reader build
make docker-reader-push        # Tag + push reader to ECR (requires REGISTRY=<ecr-uri>)
make docker-reader-deploy      # Full reader ECR pipeline: login, buildx, push
make setup-aws          # Create AWS infrastructure
make restore-env        # Copy ~/dev/etc/backflow/.env → ./.env
make backup-env         # Copy ./.env → ~/dev/etc/backflow/.env
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
- `GET /debug/stats` — Operational stats: PID, uptime, running tasks, pool metrics (outside `/api/v1/`, bearer-auth protected when API keys are configured)
- `GET /api/v1/health` — Health check (under API prefix; bearer-auth protected when API keys are configured; blocked when `BACKFLOW_RESTRICT_API=true`)
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
- **orchestrator/** — Poll loop (`orchestrator.go`), dispatch (`dispatch.go`), monitoring (`monitor.go`, including `handleReadingCompletion` for read-mode tasks), recovery (`recovery.go`), local mode (`local.go`). Subpackages: `docker/` (Docker container management via SSM or local exec), `ec2/` (EC2 lifecycle, auto-scaler, spot interruption handler), `fargate/` (ECS/Fargate runner, CloudWatch log parsing), `s3/` (agent output upload)
- **store/** — `Store` interface + PostgreSQL (`pgxpool`, goose migrations). Includes `CreateReading` / `UpsertReading` for the `readings` table.
- **models/** — `Task`, `Instance`, `AllowedSender`, `DiscordInstall`, and `Reading` (+ `Connection`) structs with status enums. `FindFirstURL` / `InferReviewMode` auto-detect review mode when a prompt's first URL is a GitHub PR URL.
- **embeddings/** — Thin `Embedder` interface (`Embed(ctx, text) ([]float32, error)`) with an `OpenAIEmbedder` HTTP client (no SDK). Used by the orchestrator to embed a reading's final TL;DR before writing the `readings` row.
- **discord/** — Discord interaction handler (Ed25519 signature verification, PING/PONG, interaction routing, `/backflow create` modal for task creation, `/backflow cancel` and `/backflow retry` commands, button click handling). `HandlerActions` struct groups callback functions and role-based authorization config.
- **config/** — Env-var config (`BACKFLOW_*` prefix), three modes (`ec2`/`local`/`fargate`). `RestrictAPI` blocks all `/api/v1/*` endpoints when `BACKFLOW_RESTRICT_API=true` (used in Fly.io deployment). `BACKFLOW_API_KEY` enables single-token API auth; otherwise `api_keys` in Postgres can back authenticated API/debug requests. `TaskDefaults(taskMode)` returns resolved defaults — for `read` mode it swaps in `ReaderImage` plus the `BACKFLOW_DEFAULT_READ_MAX_*` caps. `Apply(task, overrides)` fills zero-value fields using `*bool` overrides (nil = use default, non-nil = use pointed value)
- **notify/** — `Notifier` interface, `WebhookNotifier` (HTTP POST, 3 retries, event filtering), `DiscordNotifier` (lifecycle messages in channel + per-task threads), `NoopNotifier`, `EventBus` (async fan-out delivery via buffered channel), `NewEvent` constructor with `EventOption` functional options (including `WithReading` for read-mode completion events), `MessagingNotifier` (SMS via Twilio for reply channels). `Event` carries `TaskMode` plus optional reading fields (`TLDR`, `NoveltyVerdict`, `Tags`, `Connections`) populated only for read-task completion events.
- **messaging/** — `Messenger` interface, `TwilioMessenger` (outbound SMS), inbound SMS webhook handler, message parsing
- **debug/** — `/debug/stats` handler: PID, uptime, running task count, pgxpool metrics

### Fake agent (`test/blackbox/fake-agent/`)

Minimal Alpine image used by black-box and soak tests. Reads `FAKE_OUTCOME` env var to simulate outcomes: `success`, `slow_success`, `fail`, `needs_input`, `timeout`, `crash`. Writes `status.json` and emits `BACKFLOW_STATUS_JSON:` just like the real agent. Does not create `container_output.log` (soak tasks set `save_agent_output: false`).

### Soak test (`test/soak/`)

Long-running resource leak detector. Submits tasks at intervals, collects RSS, pool stats, and container counts, then analyzes for memory growth and container accumulation. Run via `make test-soak` (10-min short mode). Truncates the tasks table and prunes stale containers at start and end. The wrapper script (`scripts/test-soak.sh`) warns before truncating and asks for confirmation.

### Agent container (`docker/agent/`)

Node.js 24 image with Claude Code CLI + Codex CLI + git + gh. `entrypoint.sh`: clone → checkout → inject CLAUDE.md → run agent (with retry up to 3 attempts) → commit → push → create PR → optional self-review. Supports two harnesses: `claude_code` (`--output-format stream-json`, `--max-turns`) and `codex` (`exec --dangerously-bypass-approvals-and-sandbox`). Both harnesses work in code and review modes. Writes `status.json` for Docker-based modes and emits a `BACKFLOW_STATUS_JSON:` line for Fargate log parsing.

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

## Reading mode

When a task's `task_mode` is `read`, the orchestrator selects `BACKFLOW_READER_IMAGE` instead of the default agent image. The reader container fetches the URL in the prompt, drafts a summary, and emits structured JSON (url/title/tldr/tags/connections/novelty_verdict/etc.) to `status.json` (and a `BACKFLOW_STATUS_JSON:` log line for Fargate).

On completion, the orchestrator's `handleReadingCompletion` helper (in `internal/orchestrator/monitor.go`) runs synchronously:

1. Parses the reading-specific fields off `ContainerStatus`.
2. Calls `embeddings.Embedder.Embed(ctx, tldr)` to embed the final TL;DR (re-embedded by the orchestrator, not reused from the agent — the agent's draft TL;DR can be refined after similarity lookup).
3. Writes the row via `store.CreateReading`, or `store.UpsertReading` when `task.Force == true`.
4. Emits `task.completed` with `WithReading(tldr, verdict, tags, connections)`.

If the embedding API call or the DB write fails, the task is marked `failed` rather than silently `completed`, and `task.failed` is emitted. If `embedder` is nil (no `OPENAI_API_KEY` configured), reading tasks fail at completion.

The reading agent image and reader-side shell scripts live in `docker/reader/`; see `docs/supabase-setup.md` for the PostgREST-backed similarity-search path the agent uses during its session.

Reading-mode env vars:

- `BACKFLOW_READER_IMAGE` — Docker image for reading-mode containers
- `BACKFLOW_DEFAULT_READ_MAX_BUDGET` / `BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC` / `BACKFLOW_DEFAULT_READ_MAX_TURNS` — Tighter defaults applied by `TaskDefaults("read")`
- `OPENAI_API_KEY` — Required for the orchestrator's embeddings client (and for the reader container's `read-embed.sh`)
- `SUPABASE_URL` / `SUPABASE_ANON_KEY` — Passed to reader containers for PostgREST similarity search (see `docs/supabase-setup.md`)

The `tasks` table carries a `force` boolean column. The API input for force is not wired yet (tracked separately) — today `task.Force` is set by the orchestrator only when explicitly populated through other creation paths.

## Harnesses

- **`claude_code`** — Claude Code CLI. Requires `ANTHROPIC_API_KEY` or Max subscription credentials.
- **`codex`** — OpenAI Codex CLI. Requires `OPENAI_API_KEY`.

Configured per-task via the `harness` field or globally via `BACKFLOW_DEFAULT_HARNESS`.

PR comments include actual cost for `claude_code` (extracted from `total_cost_usd` in stream-json output). Codex CLI doesn't report cost in dollars — only raw token counts via `--json` — so cost is omitted for `codex` harness runs.

## API auth

- `BACKFLOW_API_KEY` — Optional single bearer token for API and debug access in small deployments
- `api_keys` — Postgres-backed bearer tokens with named scopes (`tasks:read`, `tasks:write`, `health:read`, `stats:read`) and optional expiration

When API keys are configured, bearer auth applies to `/api/v1/*` and `/debug/stats`. Root `/health` and webhook endpoints remain public.

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

Production app: `backflow` (`fly.toml`). Auto-deploys on push to main via `.github/workflows/ci.yml`. `BACKFLOW_RESTRICT_API=true` blocks all `/api/v1/*` endpoints; webhook paths and `/health` are unaffected.

AWS credentials for ECS/S3/CloudWatch are provided via the `backflow-fly` IAM user (created by `make setup-aws`). See `docs/fly-setup.md` for deployment steps.

## Documentation guidelines

Do not record default values for config or env vars in documentation. Defaults change frequently and docs drift silently. Instead, point to the source (`internal/config/config.go`) or say "see config for current defaults."

## Input validation

Environment variable keys passed via the `env_vars` field must match POSIX naming rules (`^[A-Za-z_][A-Za-z0-9_]*$`) and must not override reserved system keys (e.g. `ANTHROPIC_API_KEY`, `GITHUB_TOKEN`, `TASK_ID`, `SUPABASE_URL`, `SUPABASE_ANON_KEY`). See `reservedEnvVarKeys` in `internal/models/task.go` for the full list. All user-supplied text fields are validated to reject null bytes (PostgreSQL text columns reject them).

In Docker mode, secrets (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GITHUB_TOKEN`) are passed via `--env-file` rather than inline in the command string. In Fargate mode, reserved keys are blocked at validation time.

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
- `supabase-setup.md` — Supabase project setup, `readings` table, `reader` schema for PostgREST, and the publishable-key model used by the reader container
