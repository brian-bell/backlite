# CLAUDE.md

## What This Is

Backflow is a Go service that runs Claude Code in ephemeral Docker containers on AWS EC2 spot instances. Tasks come in via REST API; the orchestrator provisions infrastructure, runs agents, and cleans up.

## Commands

```bash
make build          # Build to bin/backflow
make run            # Build + run (sources .env)
make test           # go test ./... -v -count=1
make lint           # go vet ./...
make deps           # go mod tidy
make db-status      # Dump SQLite state
make docker-deploy  # Build + push agent image to ECR
make setup-aws      # Create AWS infrastructure
```

Single test: `go test ./internal/store/ -run TestCreateTask -v`

## Architecture

Two goroutines: chi REST API on `:8080` + polling orchestrator (5s default).

**Flow:** Client → API → SQLite → Orchestrator → Docker on EC2 via SSM → Webhooks.

### Key modules (`internal/`)

- **api/** — chi router, handlers, JSON responses
- **orchestrator/** — Poll loop, EC2 scaling, Docker via SSM, spot interruption handling
- **store/** — `Store` interface + SQLite (WAL mode, auto-migrated)
- **models/** — `Task` and `Instance` structs with status enums
- **config/** — Env-var config (`BACKFLOW_*` prefix)
- **notify/** — Webhook notifier

### Agent container (`docker/`)

Node.js image with Claude Code CLI + git + gh. `entrypoint.sh`: clone → checkout → run Claude → commit → push → create PR. Writes `status.json` for the orchestrator.

### Statuses

- **Task:** `pending` → `provisioning` → `running` → `completed` | `failed` | `interrupted` | `cancelled`
- **Instance:** `pending` → `running` → `draining` → `terminated`

## Auth modes

- **`api_key`** — Anthropic API key, concurrent agents
- **`max_subscription`** — Claude Max, serial (one agent at a time)

## Design patterns

- Interface abstractions (`Store`, `Notifier`) for testability
- Polling over events for simplicity
- SSM instead of SSH (no key management)
- ULID task IDs with `bf_` prefix
- Zerolog structured logging

## Database

SQLite with WAL mode. Schema auto-migrates on startup via `CREATE TABLE IF NOT EXISTS` in `internal/store/sqlite.go:migrate()`. No separate migration files — add new columns with `ALTER TABLE` idempotently in the same function.
