# CLAUDE.md

## What This Is

Backflow is a Go service that runs coding agents (Claude Code or Codex) in ephemeral Docker containers on AWS EC2 spot instances. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

## Commands

```bash
make build              # Build to bin/backflow
make run                # Build + run (sources .env)
make test               # go test ./... -v -count=1
make lint               # go vet ./...
make deps               # go mod tidy
make clean              # Remove bin/ directory
make db-status          # Dump SQLite state
make docker-build       # Buildx multi-platform (amd64+arm64) image
make docker-build-local # Single-architecture build
make docker-push        # Tag + push to ECR (requires REGISTRY=<ecr-uri>)
make docker-deploy      # Full ECR pipeline: login, buildx, push
make setup-aws          # Create AWS infrastructure
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

## Architecture

Two goroutines: chi REST API on `:8080` + polling orchestrator (5s default). Two operating modes: `ec2` (default, spot instances) and `local` (Docker on local machine).

**Flow:** Client → API → SQLite → Orchestrator → Docker on EC2 via SSM (or local) → Webhooks.

### API endpoints (`/api/v1`)

- `GET /health` — Health check
- `POST /tasks` — Create task
- `GET /tasks` — List tasks (query params: `status`, `limit`, `offset`)
- `GET /tasks/{id}` — Get task
- `DELETE /tasks/{id}` — Cancel task (sets status to `cancelled`)
- `GET /tasks/{id}/logs` — Stream container logs

### Key modules (`internal/`)

- **api/** — chi router, handlers, JSON responses, `LogFetcher` interface
- **orchestrator/** — Poll loop (`orchestrator.go`), EC2 scaling (`ec2.go`, `scaler.go`), Docker via SSM (`docker.go`), spot interruption handling (`spot.go`), local mode (`local.go`)
- **store/** — `Store` interface + SQLite (WAL mode, auto-migrated)
- **models/** — `Task` and `Instance` structs with status enums
- **config/** — Env-var config (`BACKFLOW_*` prefix), two modes (`ec2`/`local`)
- **notify/** — `Notifier` interface, `WebhookNotifier` (HTTP POST, 3 retries, event filtering), `NoopNotifier`

### Agent container (`docker/`)

Node.js 20 image with Claude Code CLI + git + gh. `entrypoint.sh`: clone → checkout → inject CLAUDE.md → run agent (with retry up to 3 attempts) → commit → push → create PR → optional self-review. Supports two harnesses: `claude_code` (default, `--output-format stream-json`) and `codex` (`--full-auto --quiet`). Writes `status.json` for the orchestrator. Generates PR title via Claude when none is provided.

### Statuses

- **Task:** `pending` → `provisioning` → `running` → `completed` | `failed` | `interrupted` | `cancelled` | `recovering` → `pending` | `running` | `completed` | `failed`
- **Instance:** `pending` → `running` → `draining` → `terminated`

### Webhook events

`task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`, `task.recovering`

## Harnesses

- **`claude_code`** (default) — Claude Code CLI. Requires `ANTHROPIC_API_KEY` or Max subscription credentials.
- **`codex`** — OpenAI Codex CLI. Requires `OPENAI_API_KEY`. Defaults to `gpt-5.4` model.

Configured per-task via the `harness` field or globally via `BACKFLOW_DEFAULT_HARNESS`.

PR comments include actual cost for `claude_code` (extracted from `total_cost_usd` in stream-json output). Codex CLI doesn't report cost in dollars — only raw token counts via `--json` — so cost is omitted for `codex` harness runs.

## Auth modes

- **`api_key`** — Anthropic API key via `ANTHROPIC_API_KEY`, concurrent agents (max_instances × containers_per_instance)
- **`max_subscription`** — Claude Max credentials via `CLAUDE_CREDENTIALS_PATH` volume mount, serial (one agent at a time)

## Design patterns

- Interface abstractions (`Store`, `Notifier`, `LogFetcher`) for testability
- Polling over events for simplicity
- SSM instead of SSH (no key management) in EC2 mode; direct Docker exec in local mode
- ULID task IDs with `bf_` prefix
- Zerolog structured logging
- Spot interruption detection with automatic task re-queuing

## Database

SQLite with WAL mode. Schema auto-migrates on startup via `CREATE TABLE IF NOT EXISTS` in `internal/store/sqlite.go:migrate()`. No separate migration files — add new columns with `ALTER TABLE` idempotently in the same function.

## Documentation

Additional docs in `docs/`:
- `schema.md` — SQLite database schema reference
- `file-reference.md` — Codebase file reference guide
