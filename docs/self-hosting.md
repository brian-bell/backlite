# Self-Hosting Backlite

This guide takes a fresh checkout from zero to a running Backlite task on a single Docker host.

Backlite keeps the upstream `BACKFLOW_*` env var prefix for compatibility, even though the module, binary, and Docker image names now use `backlite`.

## Prerequisites

- Go 1.25+
- Docker with a running daemon
- SQLite
- `jq`
- A GitHub token that can clone the target repos and open PRs
- An Anthropic API key for `claude_code`, or an OpenAI API key for `codex`

## 1. Build the Images

From the repo root:

```bash
make docker-agent-build-local
```

If you plan to run the agent from a registry tag instead of the local default, set `BACKFLOW_AGENT_IMAGE` accordingly in `.env`.

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

Optional webhook notifier:

```bash
BACKFLOW_WEBHOOK_URL=https://your-webhook-endpoint.example
BACKFLOW_WEBHOOK_EVENTS=task.completed,task.failed,task.needs_input
```

Optional S3-compatible backup upload:

```bash
BACKFLOW_BACKUP_S3_BUCKET=your-backlite-backups
BACKFLOW_BACKUP_S3_PREFIX=sqlite/
# Optional provider controls:
# BACKFLOW_BACKUP_S3_REGION=us-east-1
# BACKFLOW_BACKUP_S3_ENDPOINT=https://s3.example.com
# BACKFLOW_BACKUP_S3_PATH_STYLE=true
```

Create or verify the bucket with:

```bash
scripts/setup-backup-bucket.sh --bucket "$BACKFLOW_BACKUP_S3_BUCKET" --prefix "$BACKFLOW_BACKUP_S3_PREFIX"
```

The setup helper does not overwrite lifecycle rules on an existing bucket. Configure retention manually for shared buckets if you need provider-side expiration.

Smoke-test the configured provider with:

```bash
scripts/smoke-s3-backup-provider.sh --bucket "$BACKFLOW_BACKUP_S3_BUCKET" --prefix "$BACKFLOW_BACKUP_S3_PREFIX"
```

The script runs phase 1 by default. Use `--phase all` for the lifecycle-safety, permission-failure, recovery, and restore-restart checks; phases 3 and 4 can switch credentials with `--failure-aws-profile` and `--recovery-aws-profile`. For Cloudflare R2, set `BACKFLOW_SMOKE_R2_ACCESS_KEY_ID` and `BACKFLOW_SMOKE_R2_SECRET_ACCESS_KEY`, then use `--phase 6 --r2-bucket-url "https://<account-id>.r2.cloudflarestorage.com/<bucket>"`.

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

Inspect the resulting artifacts:

```bash
curl -s http://localhost:8080/api/v1/tasks/<task-id>/output
curl -s http://localhost:8080/api/v1/tasks/<task-id>/output.json
ls "$BACKFLOW_DATA_DIR/tasks/<task-id>/"
```

## 5. Operational Notes

- Backlite is local-Docker-only. There is no alternate cloud runtime path.
- Webhooks are the primary task-event notifier.
- Concurrency capacity is capped by `BACKFLOW_MAX_CONTAINERS`; the orchestrator counts tasks in `provisioning`/`running` against it.
- `save_agent_output=false` disables the filesystem artifact write for a task.
- Backlite serves the REST API only; run any UI or dashboard separately.
- Local SQLite backups are on by default and write `backlite-YYYYMMDDTHHMMSSZ.sqlite.gz` artifacts plus `.meta.json` sidecars under `BACKFLOW_LOCAL_BACKUP_DIR`. Mount that directory on persistent storage. When `BACKFLOW_BACKUP_S3_BUCKET` is set, the newest valid local artifact is uploaded to S3-compatible storage and marked locally with `.upload.json`; upload failures retry independently and do not create duplicate local backups. Finalized backups older than `BACKFLOW_LOCAL_BACKUP_RETENTION_SEC` are pruned automatically with metadata and upload-marker sidecars (the newest valid backup is always preserved); set retention to `0` to keep everything. Worker state, upload state, and recent errors are exposed on `/debug/stats` under the `backup` key. Disable with `BACKFLOW_LOCAL_BACKUP_ENABLED=false` if you back the database up some other way. Backups include only the SQLite database, not `BACKFLOW_DATA_DIR`; restore is documented in the README.

## Related Docs

- [README.md](../README.md)
- [CLAUDE.md](../CLAUDE.md)
- [docs/schema.md](./schema.md)
