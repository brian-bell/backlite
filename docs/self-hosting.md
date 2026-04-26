# Self-Hosting Backlite

This guide takes a fresh checkout from zero to a running Backlite task on a single Docker host.

Backlite keeps the upstream `BACKFLOW_*` env var prefix for compatibility, even though the module, binary, and Docker image names now use `backlite`.

## Prerequisites

- Go 1.25+
- Node.js 20+ and npm (the bundled reading-library web app is built into the Go binary by `make build`; if you don't want the SPA, point `BACKFLOW_WEB_DIR` at an empty directory)
- Docker with a running daemon
- SQLite
- `jq`
- A GitHub token that can clone the target repos and open PRs
- An Anthropic API key for `claude_code`, or an OpenAI API key for `codex`

If you want `task_mode=read`, set `OPENAI_API_KEY` as well. Reader containers call Backlite's own API for duplicate and similarity lookups.

If you want emailed summaries of completed read tasks (skill-agent image + `claude_code` only), see [docs/resend-setup.md](./resend-setup.md) for the Resend API key and sender-domain DNS setup. The three env vars (`BACKFLOW_RESEND_API_KEY`, `BACKFLOW_NOTIFY_EMAIL_FROM`, `BACKFLOW_NOTIFY_EMAIL_TO`) must be set together or all unset — partial config blocks startup.

## 1. Build the Images

From the repo root:

```bash
make docker-agent-build-local
make docker-reader-build-local   # only if you want task_mode=read
```

If you plan to run the agent or reader from a registry tag instead of the local defaults, set `BACKFLOW_AGENT_IMAGE` and `BACKFLOW_READER_IMAGE` accordingly in `.env`.

## 2. Configure `.env`

Start from the example file:

```bash
cp .env.example .env
```

For a code/review-only deployment, set at least:

```bash
ANTHROPIC_API_KEY=...
GITHUB_TOKEN=...
BACKFLOW_DATABASE_PATH=/srv/backlite/backlite.db
BACKFLOW_AGENT_IMAGE=backlite-agent
BACKFLOW_DATA_DIR=/srv/backlite/data
```

For reader mode, also set:

```bash
OPENAI_API_KEY=...
BACKFLOW_READER_IMAGE=backlite-reader
BACKFLOW_DEFAULT_READ_MAX_BUDGET=<budget-usd>
BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC=<seconds>
BACKFLOW_DEFAULT_READ_MAX_TURNS=<turns>
# Optional when the default host-gateway URL does not work for reader containers:
# BACKFLOW_INTERNAL_API_BASE_URL=http://host.docker.internal:8080
```

Optional webhook notifier:

```bash
BACKFLOW_WEBHOOK_URL=https://your-webhook-endpoint.example
BACKFLOW_WEBHOOK_EVENTS=task.completed,task.failed,task.needs_input
```

Backlite auto-runs SQLite migrations on startup. It writes the application database at `BACKFLOW_DATABASE_PATH` and completed task logs and metadata under `BACKFLOW_DATA_DIR/tasks/<task-id>/`. Choose paths on persistent storage.

See `internal/config/config.go` for the full env surface and current defaults.

## 3. Start the Server

Use `make run` so the command sources `.env` before starting the binary:

```bash
make run
```

The server process needs access to the Docker socket because it launches agent containers locally. Running the server binary directly on the Docker host is the supported self-hosting path.

## 4. Smoke Test the Deployment

Check health:

```bash
curl -s http://localhost:8080/health
```

Submit a code task:

```bash
./scripts/create-task.sh "Fix the login bug in https://github.com/owner/repo"
```

Submit a review task:

```bash
./scripts/review-pr.sh https://github.com/owner/repo/pull/42
```

Submit a read task:

```bash
./scripts/read-url.sh https://example.com/article
```

Inspect the resulting artifacts:

```bash
curl -s http://localhost:8080/api/v1/tasks/<task-id>/output
curl -s http://localhost:8080/api/v1/tasks/<task-id>/output.json
ls "$BACKFLOW_DATA_DIR/tasks/<task-id>/"
```

## 5. Operational Notes

- Backlite is local-Docker-only. There is no alternate cloud runtime path.
- Webhooks are the primary task-event notifier; the optional Resend integration only emails read-mode summaries.
- Concurrency capacity is capped by `BACKFLOW_MAX_CONTAINERS`; the orchestrator counts tasks in `provisioning`/`running` against it.
- `save_agent_output=false` disables the filesystem artifact write for a task.
- The reading-library SPA is served at `/`. Visit `http://<host>:8080/` after `make build && make run` to browse stored readings; paste a bearer token into the topbar form when API keys are configured.

## Related Docs

- [README.md](../README.md)
- [CLAUDE.md](../CLAUDE.md)
- [docs/schema.md](./schema.md)
