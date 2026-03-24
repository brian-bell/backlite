# Database Schema

Backflow uses PostgreSQL (hosted on Supabase, connected via session pooler). Migrations are managed by [goose](https://github.com/pressly/goose) and live in `migrations/`. The connection string is set via `BACKFLOW_DATABASE_URL`.

## Tables

### `tasks`

Stores agent tasks submitted via the REST API.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `id` | `TEXT` | — | **Primary key.** ULID with `bf_` prefix (e.g. `bf_01KKQW82994E87Z99QVEMBN8V0`). |
| `status` | `TEXT` | `'pending'` | Task lifecycle state. One of: `pending`, `provisioning`, `running`, `completed`, `failed`, `interrupted`, `cancelled`, `recovering`. |
| `task_mode` | `TEXT` | `'code'` | Task mode. `code` (default) or `review` (PR review). |
| `harness` | `TEXT` | `'claude_code'` | Agent CLI harness. `claude_code` (default) or `codex`. |
| `repo_url` | `TEXT` | — | Git repository URL to clone (required). |
| `branch` | `TEXT` | `''` | Branch to check out before running the agent. |
| `target_branch` | `TEXT` | `''` | Base branch for PR creation (e.g. `main`). |
| `prompt` | `TEXT` | — | The instruction given to the agent (required). |
| `context` | `TEXT` | `''` | Additional context appended to the prompt. |
| `model` | `TEXT` | `''` | Model override (e.g. `claude-sonnet-4-6`, `gpt-5.4`). |
| `effort` | `TEXT` | `''` | Agent effort level. One of: `low`, `medium`, `high`, `xhigh`, or empty for default. |
| `max_budget_usd` | `DOUBLE PRECISION` | `0` | Maximum spend in USD. 0 = unlimited. |
| `max_runtime_sec` | `INTEGER` | `0` | Maximum wall-clock runtime in seconds. 0 = unlimited. |
| `max_turns` | `INTEGER` | `0` | Maximum agent conversation turns. 0 = unlimited. |
| `create_pr` | `BOOLEAN` | `false` | Whether to create a pull request on completion. |
| `self_review` | `BOOLEAN` | `false` | Whether the agent self-reviews before finishing. |
| `save_agent_output` | `BOOLEAN` | `true` | Whether to upload agent output to S3. |
| `pr_title` | `TEXT` | `''` | Pull request title (if `create_pr` is set). |
| `pr_body` | `TEXT` | `''` | Pull request body/description. |
| `pr_url` | `TEXT` | `''` | URL of the created PR (populated after completion). |
| `output_url` | `TEXT` | `''` | S3 URL of the uploaded agent output. |
| `allowed_tools` | `JSONB` | `'[]'` | JSON array of allowed Claude Code tool names. |
| `claude_md` | `TEXT` | `''` | Custom CLAUDE.md content injected into the agent container. |
| `env_vars` | `JSONB` | `'{}'` | JSON object of environment variables passed to the container. |
| `instance_id` | `TEXT` | `''` | EC2 instance ID where the container runs. |
| `container_id` | `TEXT` | `''` | Docker container ID on the assigned instance. |
| `retry_count` | `INTEGER` | `0` | Number of times this task has been re-queued (e.g. after spot interruption). |
| `cost_usd` | `DOUBLE PRECISION` | `0` | Tracked cost in USD. |
| `elapsed_time_sec` | `INTEGER` | `0` | Wall-clock seconds the agent ran. |
| `error` | `TEXT` | `''` | Error message if the task failed. |
| `reply_channel` | `TEXT` | `''` | Messaging reply channel (e.g. `sms:+15551234567`). Set when task is created via SMS. |
| `created_at` | `TIMESTAMPTZ` | `now()` | When the task was created. |
| `updated_at` | `TIMESTAMPTZ` | `now()` | Last modification time. |
| `started_at` | `TIMESTAMPTZ` | `NULL` | When the agent container started. Nullable. |
| `completed_at` | `TIMESTAMPTZ` | `NULL` | When the task reached a terminal state. Nullable. |

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
| `created_at` | `TIMESTAMPTZ` | `now()` | When the instance record was created. |
| `updated_at` | `TIMESTAMPTZ` | `now()` | Last modification time. |

**Indexes:**
- `idx_instances_status` on `status` — used to find running/pending instances for task dispatch.

### `allowed_senders`

Pre-registered senders authorized to create tasks via messaging (e.g. SMS).

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `channel_type` | `TEXT` | — | **Composite PK.** Messaging channel type (e.g. `sms`). |
| `address` | `TEXT` | — | **Composite PK.** Sender address (e.g. `+15551234567`). |
| `enabled` | `BOOLEAN` | `true` | Whether this sender is allowed to create tasks. |
| `created_at` | `TIMESTAMPTZ` | `now()` | When the sender was registered. |

**Primary key:** `(channel_type, address)`

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

### `discord_installs`

Stores Discord bot installation state per guild. Seeded from config at startup; survives restarts.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `guild_id` | `TEXT` | — | **Primary key.** Discord server (guild) ID. |
| `app_id` | `TEXT` | — | Discord application ID. |
| `channel_id` | `TEXT` | — | Target channel for notifications. |
| `allowed_roles` | `JSONB` | `'[]'` | Role IDs authorized for mutation commands. |
| `installed_at` | `TIMESTAMPTZ` | `now()` | When the install record was first created. |
| `updated_at` | `TIMESTAMPTZ` | `now()` | Last config update. |

### `discord_task_threads`

Stores the Discord root message and thread IDs for each task so lifecycle updates can continue in the same thread after restarts.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `task_id` | `TEXT` | — | **Primary key.** Backflow task ID. |
| `root_message_id` | `TEXT` | — | Discord message ID of the root lifecycle post in the channel. |
| `thread_id` | `TEXT` | — | Discord thread ID used for subsequent lifecycle updates. |
| `created_at` | `TIMESTAMPTZ` | `now()` | When the mapping was first created. |
| `updated_at` | `TIMESTAMPTZ` | `now()` | Last update time. |

## Notes

- All timestamps use `TIMESTAMPTZ` and default to `now()`. Nullable timestamps (`started_at`, `completed_at`) are NULL until set.
- Booleans use native PostgreSQL `BOOLEAN` type.
- JSON fields (`allowed_tools`, `env_vars`) use `JSONB` for indexed/queryable storage.
- Schema migrations are managed by goose in `migrations/`.

## Migration Workflow

1. Inspect `migrations/` and determine the next numeric prefix.
2. Create `NNN_slug.sql` with `-- +goose Up` and `-- +goose Down` sections.
3. Use Postgres-native types that match the existing schema (`TIMESTAMPTZ`, `BOOLEAN`, `JSONB`, `DOUBLE PRECISION`, `TEXT`, `INTEGER`).
4. Apply with `goose -dir migrations up`, inspect with `goose -dir migrations status`, and roll back with `goose -dir migrations down`.
