# Database Schema

Backflow uses SQLite in WAL mode with foreign keys enabled. The schema is auto-migrated on startup via `internal/store/sqlite.go:migrate()` using `CREATE TABLE IF NOT EXISTS` statements.

Connection string: `<path>?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on`

## Tables

### `tasks`

Stores agent tasks submitted via the REST API.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `id` | `TEXT` | — | **Primary key.** ULID with `bf_` prefix (e.g. `bf_01KKQW82994E87Z99QVEMBN8V0`). |
| `status` | `TEXT` | `'pending'` | Task lifecycle state. One of: `pending`, `provisioning`, `running`, `completed`, `failed`, `interrupted`, `cancelled`, `recovering`. |
| `task_mode` | `TEXT` | `'code'` | Task mode. `code` (default) or `review` (PR review). Added via migration. |
| `harness` | `TEXT` | `'claude_code'` | Agent CLI harness. `claude_code` (default) or `codex`. Added via migration. |
| `repo_url` | `TEXT` | — | Git repository URL to clone (required). |
| `branch` | `TEXT` | `''` | Branch to check out before running the agent. |
| `target_branch` | `TEXT` | `''` | Base branch for PR creation (e.g. `main`). |
| `review_pr_number` | `INTEGER` | `0` | PR number to review (used when `task_mode` is `review`). Added via migration. |
| `prompt` | `TEXT` | — | The instruction given to the agent (required). |
| `context` | `TEXT` | `''` | Additional context appended to the prompt. |
| `model` | `TEXT` | `''` | Model override (e.g. `claude-sonnet-4-6`, `gpt-5.4`). |
| `effort` | `TEXT` | `''` | Agent effort level. One of: `low`, `medium`, `high`, `xhigh`, or empty for default. |
| `max_budget_usd` | `REAL` | `0` | Maximum spend in USD. 0 = unlimited. |
| `max_runtime_min` | `INTEGER` | `0` | Maximum wall-clock runtime in minutes. 0 = unlimited. |
| `max_turns` | `INTEGER` | `0` | Maximum agent conversation turns. 0 = unlimited. |
| `create_pr` | `INTEGER` | `0` | Boolean (0/1). Whether to create a pull request on completion. |
| `self_review` | `INTEGER` | `0` | Boolean (0/1). Whether the agent self-reviews before finishing. |
| `pr_title` | `TEXT` | `''` | Pull request title (if `create_pr` is set). |
| `pr_body` | `TEXT` | `''` | Pull request body/description. |
| `pr_url` | `TEXT` | `''` | URL of the created PR (populated after completion). |
| `allowed_tools` | `TEXT` | `'[]'` | JSON array of allowed Claude Code tool names. |
| `claude_md` | `TEXT` | `''` | Custom CLAUDE.md content injected into the agent container. |
| `env_vars` | `TEXT` | `'{}'` | JSON object of environment variables passed to the container. |
| `instance_id` | `TEXT` | `''` | EC2 instance ID where the container runs. |
| `container_id` | `TEXT` | `''` | Docker container ID on the assigned instance. |
| `retry_count` | `INTEGER` | `0` | Number of times this task has been re-queued (e.g. after spot interruption). |
| `cost_usd` | `REAL` | `0` | Tracked cost in USD. |
| `error` | `TEXT` | `''` | Error message if the task failed. |
| `created_at` | `TEXT` | — | RFC 3339 timestamp. When the task was created. |
| `updated_at` | `TEXT` | — | RFC 3339 timestamp. Last modification time. |
| `started_at` | `TEXT` | `NULL` | RFC 3339 timestamp. When the agent container started. Nullable. |
| `completed_at` | `TEXT` | `NULL` | RFC 3339 timestamp. When the task reached a terminal state. Nullable. |

**Indexes:**
- `idx_tasks_status` on `status` — used by the orchestrator to find pending/running tasks.
- `idx_tasks_created` on `created_at` — used for ordered listing.

### `instances`

Tracks EC2 spot instances managed by the orchestrator.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `instance_id` | `TEXT` | — | **Primary key.** AWS EC2 instance ID (e.g. `i-0abc123def456`). |
| `instance_type` | `TEXT` | — | EC2 instance type (e.g. `c6g.2xlarge`). |
| `availability_zone` | `TEXT` | `''` | AWS AZ (e.g. `us-east-1a`). |
| `private_ip` | `TEXT` | `''` | Instance private IP address. |
| `status` | `TEXT` | `'pending'` | Instance lifecycle state. One of: `pending`, `running`, `draining`, `terminated`. |
| `max_containers` | `INTEGER` | `4` | Maximum concurrent agent containers on this instance. |
| `running_containers` | `INTEGER` | `0` | Current number of running containers. |
| `created_at` | `TEXT` | — | RFC 3339 timestamp. When the instance record was created. |
| `updated_at` | `TEXT` | — | RFC 3339 timestamp. Last modification time. |

**Indexes:**
- `idx_instances_status` on `status` — used to find running/pending instances for task dispatch.

## Status Lifecycles

### Task statuses

```
pending → provisioning → running → completed
                                  → failed
                                  → interrupted → (re-queued as pending)
         (any non-terminal)      → cancelled
running/provisioning → recovering → running (container still alive)
                                  → completed/failed (container exited)
                                  → pending (re-queued, container/instance gone)
```

Terminal states: `completed`, `failed`, `cancelled`.

The `recovering` status is set on startup for tasks orphaned by a server restart. The orchestrator inspects their containers and resolves them on each tick.

### Instance statuses

```
pending → running → draining → terminated
                  → terminated
```

## Notes

- All timestamps are stored as RFC 3339 strings, not SQLite datetime types.
- Booleans (`create_pr`, `self_review`) are stored as integers (0/1).
- JSON fields (`allowed_tools`, `env_vars`) are stored as serialized TEXT.
- Schema changes are applied idempotently in `migrate()` — new columns use `ALTER TABLE ... ADD COLUMN` with `IF NOT EXISTS` semantics.
