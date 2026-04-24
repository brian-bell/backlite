# Database Schema

Backlite uses a local SQLite database. Migrations are managed by [goose](https://github.com/pressly/goose) and live in `migrations/`. The database path is set via `BACKFLOW_DATABASE_PATH`.

## Tables

### `tasks`

Stores agent tasks submitted via the REST API.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `id` | `TEXT` | — | **Primary key.** ULID with `bf_` prefix (e.g. `bf_01KKQW82994E87Z99QVEMBN8V0`). |
| `status` | `TEXT` | `'pending'` | Task lifecycle state. One of: `pending`, `provisioning`, `running`, `completed`, `failed`, `interrupted`, `cancelled`, `recovering`. |
| `task_mode` | `TEXT` | `'auto'` | Task mode. One of: `code`, `review`, `read`, or `auto` (Prep stage infers code vs review). |
| `harness` | `TEXT` | `'claude_code'` | Agent CLI harness. `claude_code` (default) or `codex`. |
| `repo_url` | `TEXT` | — | Git repository URL to clone (required). |
| `branch` | `TEXT` | `''` | Branch to check out before running the agent. |
| `target_branch` | `TEXT` | `''` | Base branch for PR creation (e.g. `main`). |
| `prompt` | `TEXT` | — | The instruction given to the agent (required). |
| `context` | `TEXT` | `''` | Additional context appended to the prompt. |
| `model` | `TEXT` | `''` | Model override (e.g. `claude-sonnet-4-6`, `gpt-5.4`). |
| `effort` | `TEXT` | `''` | Agent effort level. One of: `low`, `medium`, `high`, `xhigh`, or empty for default. |
| `max_budget_usd` | `REAL` | `0` | Maximum spend in USD. 0 = unlimited. |
| `max_runtime_sec` | `INTEGER` | `0` | Maximum wall-clock runtime in seconds. 0 = unlimited. |
| `max_turns` | `INTEGER` | `0` | Maximum agent conversation turns. 0 = unlimited. |
| `create_pr` | `BOOLEAN` | `false` | Whether to create a pull request on completion. |
| `self_review` | `BOOLEAN` | `false` | Whether the agent self-reviews before finishing. |
| `save_agent_output` | `BOOLEAN` | `true` | Whether to persist agent output for the `/output` and `/output.json` endpoints. |
| `pr_title` | `TEXT` | `''` | Pull request title (if `create_pr` is set). |
| `pr_body` | `TEXT` | `''` | Pull request body/description. |
| `pr_url` | `TEXT` | `''` | URL of the created PR (populated after completion). |
| `output_url` | `TEXT` | `''` | API-relative URL of the persisted agent output log. |
| `allowed_tools` | `TEXT` | `'[]'` | JSON array of allowed Claude Code tool names. |
| `claude_md` | `TEXT` | `''` | Custom CLAUDE.md content injected into the agent container. |
| `env_vars` | `TEXT` | `'{}'` | JSON object of environment variables passed to the container. |
| `instance_id` | `TEXT` | `''` | FK-like reference to `instances.instance_id` — `'local'` in the current local-Docker runtime. |
| `container_id` | `TEXT` | `''` | Docker container ID on the assigned instance. |
| `retry_count` | `INTEGER` | `0` | Number of times this task has been re-queued (includes both auto-requeues and user retries). |
| `user_retry_count` | `INTEGER` | `0` | Number of user-initiated retries (separate from auto-requeues like spot interruption). Capped by `BACKFLOW_MAX_USER_RETRIES`. |
| `cost_usd` | `REAL` | `0` | Tracked cost in USD. |
| `elapsed_time_sec` | `INTEGER` | `0` | Wall-clock seconds the agent ran. |
| `error` | `TEXT` | `''` | Error message if the task failed. |
| `ready_for_retry` | `BOOLEAN` | `false` | Whether the task is ready for user retry. Set `true` after container cleanup completes (for failed/cancelled/interrupted tasks under the retry cap). Reset to `false` on requeue. |
| `reply_channel` | `TEXT` | `''` | Messaging reply channel. Legacy field from the removed SMS/Discord integrations; kept on the column set but no longer populated. |
| `agent_image` | `TEXT` | `''` | Docker image the orchestrator used for this task's container. Populated at creation time — code/review tasks get the default agent image; read tasks get `BACKFLOW_READER_IMAGE`. Not user-settable via the API. |
| `force` | `BOOLEAN` | `false` | For reading tasks, skip the exact-URL duplicate check and upsert the existing `readings` row on completion. Ignored for `code`/`review` tasks. |
| `created_at` | `TEXT` | current UTC timestamp | When the task was created. |
| `updated_at` | `TEXT` | current UTC timestamp | Last modification time. |
| `started_at` | `TEXT` | `NULL` | When the agent container started. Nullable. |
| `completed_at` | `TEXT` | `NULL` | When the task reached a terminal state. Nullable. |

**Indexes:**
- `idx_tasks_status` on `status` — used by the orchestrator to find pending/running tasks.
- `idx_tasks_created` on `created_at` — used for ordered listing.

### `instances`

Tracks the container-execution slots the orchestrator dispatches against. With the current local-Docker runtime there is a single synthetic row with `instance_id = 'local'` created on startup by `Orchestrator.initInstance()`; `running_containers` is the live concurrency counter and `max_containers` comes from `BACKFLOW_CONTAINERS_PER_INSTANCE`.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `instance_id` | `TEXT` | — | **Primary key.** `'local'` in the current runtime. |
| `status` | `TEXT` | `'pending'` | Instance lifecycle state. One of: `pending`, `running`, `draining`, `terminated`. |
| `max_containers` | `INTEGER` | `4` | Maximum concurrent agent containers on this instance. |
| `running_containers` | `INTEGER` | `0` | Current number of running containers. |
| `created_at` | `TEXT` | current UTC timestamp | When the instance record was created. |
| `updated_at` | `TEXT` | current UTC timestamp | Last modification time. |

**Indexes:**
- `idx_instances_status` on `status` — used to find running/pending instances for task dispatch.

### `api_keys`

Stores bearer tokens used to authenticate API and debug requests.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `key_hash` | `TEXT` | — | **Primary key.** SHA-256 hash of the bearer token. The raw token is never stored. |
| `name` | `TEXT` | `''` | Human-readable label for the key. |
| `permissions` | `TEXT` | `'[]'` | JSON array of scope strings such as `tasks:read`, `tasks:write`, `health:read`, and `stats:read`. |
| `expires_at` | `TEXT` | `NULL` | Optional expiration timestamp. Expired keys are rejected. |
| `created_at` | `TEXT` | current UTC timestamp | When the key record was created. |
| `updated_at` | `TEXT` | current UTC timestamp | Last modification time. |

**Indexes:**
- `idx_api_keys_expires_at` on `expires_at` — used to support expiration checks and cleanup.

### `readings`

Structured output of completed `task_mode=read` tasks. Populated by the orchestrator's `handleReadingCompletion` helper.

| Column | Type | Default | Description |
|--------|------|---------|-------------|
| `id` | `TEXT` | — | **Primary key.** ULID with `bf_` prefix. |
| `task_id` | `TEXT` | — | Foreign key to `tasks(id)`, `ON DELETE CASCADE`. |
| `url` | `TEXT` | — | Source URL. `UNIQUE` index for duplicate lookups and upsert. |
| `title` | `TEXT` | `''` | Page title as reported by the reader agent. |
| `tldr` | `TEXT` | `''` | Short summary. The orchestrator embeds this text to populate `embedding`. |
| `tags` | `TEXT` | `'[]'` | JSON array of topic tags from the agent. |
| `keywords` | `TEXT` | `'[]'` | JSON array of salient keywords. |
| `people` | `TEXT` | `'[]'` | JSON array of people named in the article. |
| `orgs` | `TEXT` | `'[]'` | JSON array of organizations named in the article. |
| `novelty_verdict` | `TEXT` | `''` | Agent's judgment relative to existing readings (`new`, `nothing new`, etc.). |
| `connections` | `TEXT` | `'[]'` | JSON array of `{reading_id, reason}` pointing at similar prior readings. |
| `summary` | `TEXT` | `''` | Full markdown summary. |
| `raw_output` | `TEXT` | `'{}'` | Lossless JSON of the agent's parsed `status.json`, kept for future re-normalization. |
| `embedding` | `TEXT` | `''` | JSON-encoded OpenAI `text-embedding-3-small` vector of the final TL;DR. Embedded by the orchestrator, not the agent. |
| `created_at` | `TEXT` | current UTC timestamp | When the reading was stored. |

**Indexes:**
- Unique `idx_readings_url` on `url` — duplicate detection and upsert.

Similarity search is computed in the application layer by decoding stored embeddings and ranking cosine similarity in Go.

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

- All timestamps are stored as UTC RFC3339 strings. Nullable timestamps (`started_at`, `completed_at`) are NULL until set.
- Booleans use SQLite `BOOLEAN` affinity.
- JSON fields (`allowed_tools`, `env_vars`, reading arrays/objects) are stored as JSON text.
- API key secrets are stored as SHA-256 hashes in `api_keys.key_hash`; scope membership is stored in `permissions`.
- Schema migrations are managed by goose in `migrations/`.

## Migration Workflow

1. Inspect `migrations/` and determine the next numeric prefix.
2. Create `NNN_slug.sql` with `-- +goose Up` and `-- +goose Down` sections.
3. Use SQLite-compatible types that match the existing schema (`TEXT`, `INTEGER`, `REAL`, `BOOLEAN`).
4. Apply with `goose -dir migrations up`, inspect with `goose -dir migrations status`, and roll back with `goose -dir migrations down`.
