# File Reference

Complete mapping of every file in the Backflow repository.

## Root

| File | Description |
|------|-------------|
| `CLAUDE.md` | Project instructions for Claude Code — architecture overview, commands, design patterns |
| `Makefile` | Build, test, lint, Docker, and deployment targets |
| `README.md` | Project documentation — quickstart, API reference, configuration, architecture diagram |
| `.env.example` | Sample environment variables with defaults and comments |
| `.gitignore` | Ignores `bin/`, `.db` files, `.env`, and `mise.toml` |
| `go.mod` | Go module definition (`github.com/backflow-labs/backflow`, Go 1.24.1) with dependencies |
| `go.sum` | Go module checksums |

## `cmd/backflow/`

Entry point for the server binary.

| File | Description |
|------|-------------|
| `main.go` | Application entry point. Loads config, opens SQLite, initializes the notifier, orchestrator, and HTTP server, then runs both the orchestrator poll loop and the chi-based API server as concurrent goroutines. Handles graceful shutdown on SIGINT/SIGTERM. |

## `internal/api/`

REST API layer built on the chi router.

| File | Description |
|------|-------------|
| `server.go` | Creates the chi router with middleware (RequestID, RealIP, Logger, Recoverer, JSON content-type) and registers all `/api/v1` routes: health check, and CRUD + logs endpoints for tasks. |
| `handlers.go` | HTTP handler methods for the API. `CreateTask` validates input and writes to the store. `GetTask`, `ListTasks`, `DeleteTask` handle retrieval, listing with filters (status/limit/offset), and cancellation or deletion. `GetTaskLogs` fetches live container logs via the `LogFetcher` interface. `HealthCheck` returns status and auth mode. |
| `handlers_test.go` | Tests for API handlers — health check, create/get task, list tasks, input validation (including harness validation), codex harness default model selection, 404 on missing task, and delete. Uses an in-memory SQLite store and `httptest`. |
| `responses.go` | JSON response helpers. Defines the `envelope` struct (`{data, error}`) and `writeJSON`/`writeError` functions used by all handlers. |

## `internal/config/`

Environment-variable-based configuration.

| File | Description |
|------|-------------|
| `config.go` | Defines the `Config` struct with all server settings (mode, auth, AWS, agent defaults, webhooks, DB, polling). `Load()` reads from environment variables with sensible defaults. Supports two modes (`ec2`, `local`), two auth modes (`api_key`, `max_subscription`), and two harnesses (`claude_code`, `codex`) with per-harness default models. `MaxConcurrent()` computes the concurrency limit based on auth mode, dispatch mode, and instance capacity. |

## `internal/models/`

Data structures and status enums.

| File | Description |
|------|-------------|
| `task.go` | `Task` struct with all fields (ID, status, task_mode, harness, repo, branch, prompt, model, effort, budget, runtime, turns, PR info, container/instance IDs, cost, timestamps). `Harness` type (`claude_code`, `codex`). `TaskMode` constants (`code`, `review`). `CreateTaskRequest` struct with `Validate()` for API input. `TaskStatus` enum: `pending`, `provisioning`, `running`, `completed`, `failed`, `interrupted`, `cancelled`, `recovering`. Helper methods `IsTerminal()`, `AllowedToolsJSON()`, `EnvVarsJSON()`. |
| `task_test.go` | Table-driven tests for `CreateTaskRequest.Validate()` (valid input, missing fields, negative budget, harness validation, task mode validation) and `TaskStatus.IsTerminal()` for all status values. |
| `instance.go` | `Instance` struct (instance ID, type, AZ, IP, status, container counts, timestamps). `InstanceStatus` enum: `pending`, `running`, `draining`, `terminated`. |

## `internal/notify/`

Notification system for task lifecycle events.

| File | Description |
|------|-------------|
| `webhook.go` | `Notifier` interface with a `Notify(Event)` method. `NoopNotifier` discards events. `WebhookNotifier` sends HTTP POST requests with JSON payloads, supports event filtering, and retries up to 3 times with backoff. Defines event types: `task.created`, `task.running`, `task.completed`, `task.failed`, `task.needs_input`, `task.interrupted`. |

## `internal/orchestrator/`

Core orchestration loop and infrastructure management.

| File | Description |
|------|-------------|
| `orchestrator.go` | Main orchestrator. `New()` initializes sub-components based on mode (EC2 vs local). `Start()` runs the poll loop on a configurable interval. Each `tick()` delegates to `monitor.go`, `dispatch.go`, `recovery.go`, and `scaler.Evaluate()`. |
| `dispatch.go` | Task dispatch logic. Finds pending tasks, assigns them to instances with capacity, and starts containers. |
| `dispatch_test.go` | Tests for dispatch logic. |
| `monitor.go` | Running task monitoring. Checks container status, detects timeouts, handles completions/failures, and manages cancellations. |
| `monitor_test.go` | Tests for monitoring logic. |
| `recovery.go` | Startup recovery for orphaned tasks. Detects tasks left in `running`/`provisioning` state after a server restart, marks them as `recovering`, and resolves them by inspecting containers. |
| `recovery_test.go` | Tests for recovery logic. |
| `helpers_test.go` | Shared test helpers for orchestrator tests. |
| `scaler.go` | EC2 instance auto-scaling. Defines the `scaler` interface (`Evaluate`, `RequestScaleUp`). `Scaler` implements it for EC2 mode: launches spot instances when capacity is needed, waits for SSM + Docker readiness before marking instances as running, detects externally terminated instances, and terminates idle instances after 5 minutes. |
| `docker.go` | Container lifecycle management via SSM (EC2 mode) or local shell (local mode). `RunAgent()` builds `docker run` commands with environment variables for task config (including harness and task mode), auth credentials (Anthropic, OpenAI, GitHub). `InspectContainer()` checks container state and reads `status.json`. `StopContainer()` and `GetLogs()` wrap Docker commands. `runSSMCommand()` executes commands on remote EC2 instances via AWS SSM `SendCommand`. |
| `ec2.go` | EC2 API wrapper. `LaunchSpotInstance()` creates one-time spot instances using either a launch template or AMI + instance type. `TerminateInstance()` and `DescribeInstance()` wrap the corresponding EC2 API calls. Lazy-initializes the AWS EC2 client. |
| `local.go` | No-op `localScaler` struct that satisfies the `scaler` interface. Used in local mode where no EC2 instances need management. |
| `spot.go` | Spot interruption handler. `CheckInterruptions()` polls running instances for termination signals. `handleInterruption()` marks the instance as draining and re-queues all running tasks on that instance back to `pending` with an incremented retry count. |

## `internal/store/`

Persistence layer.

| File | Description |
|------|-------------|
| `store.go` | `Store` interface defining CRUD operations for tasks (`Create`, `Get`, `List`, `Update`, `Delete`) and instances (`Create`, `Get`, `List`, `Update`), plus `Close()`. `TaskFilter` struct for list queries with status filter, limit, and offset. |
| `sqlite.go` | SQLite implementation of `Store`. Opens the database in WAL mode with busy timeout and foreign keys. `migrate()` creates the `tasks` and `instances` tables with indexes on status and created_at. Implements all CRUD operations with full field scanning, JSON serialization for `allowed_tools` and `env_vars`, and RFC3339 timestamp handling. |
| `sqlite_test.go` | Tests for SQLite store — full task CRUD cycle (create, get, update, list with filter, delete) including harness and task_mode fields, review task CRUD, not-found handling, and instance CRUD (create, get, update, list by status). Uses temp DB files with cleanup. |

## `docker/`

Agent container image.

| File | Description |
|------|-------------|
| `Dockerfile` | Multi-arch Docker image based on `node:20-slim`. Installs git, curl, jq, Python 3, GitHub CLI, and Claude Code CLI (`@anthropic-ai/claude-code`). Creates an `agent` user, configures git defaults, and copies the entrypoint script. |
| `entrypoint.sh` | Agent lifecycle script run inside each container. Supports two modes: `code` (default) and `review` (PR review). Supports two harnesses: `claude_code` (stream-json output) and `codex` (plain text output). Clones the repo (depth 50), checks out the target branch, creates a working branch, optionally injects CLAUDE.md content, runs the selected harness with retries (up to 3 attempts), parses output for completion/needs-input/error status, writes `status.json`, and optionally creates a PR + self-review. |

## `scripts/`

Operational and development helper scripts.

| File | Description |
|------|-------------|
| `build-agent-image.sh` | Builds and pushes the multi-arch agent Docker image to ECR. Authenticates with ECR, creates a buildx builder, and pushes with `linux/amd64,linux/arm64` platforms. |
| `create-task.sh` | CLI helper to submit tasks via the REST API. Accepts repo URL and prompt as positional args, plus flags for branch, model, effort, budget, runtime, turns, PR options, CLAUDE.md injection, context, and env vars. Builds a JSON payload with `jq` and posts to the API with `curl`. |
| `db-status.sh` | Dumps the SQLite database state. Shows all tasks, task status summary, all instances, and instance status summary using `sqlite3` queries. |
| `setup-aws.sh` | One-time AWS infrastructure setup. Creates an ECR repository, IAM role with SSM and ECR policies, instance profile, security group (outbound-only), and launch template with user-data. Outputs the launch template ID for `.env` configuration. |
| `user-data.sh` | EC2 instance bootstrap script (run via launch template user-data). Installs Docker and SSM agent, authenticates with ECR using IMDSv2, and pulls the `backflow-agent` image. |
