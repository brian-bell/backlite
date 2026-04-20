# backlite: strip AWS, keep Supabase

## Context

`backflow-lite` is a self-hostable fork for users who want the coding-agent orchestrator without AWS. The mainline runs agents on EC2 (SSM), Fargate, or local Docker, persists to Supabase Postgres, offloads agent outputs to S3, and deploys to Fly.io. Lite keeps the Supabase-backed `Store` and the local Docker runner — everything else AWS-specific is removed, and agent outputs are written to the host filesystem.

User decisions captured up-front:
- Persistence stays on Supabase (tasks, readings, api_keys, pgvector similarity search all intact).
- Reader mode and the `readings` pipeline stay.
- Only the **webhook** notifier ships — Discord and SMS integrations are dropped.
- `fly.toml` + Fly deploy path dropped; `cmd/migrate-to-postgres` dropped.
- Soak and blackbox tests are kept; they must continue to run against Postgres + local Docker.

## Shape of the fork

Exactly one runtime mode: local Docker on the host. `BACKFLOW_MODE` goes away; `Config.Mode`, the mode switch in `cmd/backflow/main.go:136–148`, the mode branching in `internal/orchestrator/docker/command.go:19`, and the synthetic-instance switching in `orchestrator.go:54–99` all collapse.

## Delete

Whole packages / directories (no replacements, no shims):

- `internal/orchestrator/ec2/` (ec2.go, scaler.go, spot.go)
- `internal/orchestrator/fargate/`
- `internal/orchestrator/s3/`  *(replaced by filesystem writer, see below)*
- `internal/discord/`
- `internal/messaging/`
- `internal/notify/discord.go`, `internal/notify/messaging.go` and their tests
- `cmd/migrate-to-postgres/`
- `docker/reader/` — **keep**; reader mode stays.
- `fly.toml`, `docs/fly-setup.md`, `.github/workflows/ci.yml` Fly-deploy job (keep the test job)

Files that lose their reason to exist:

- `internal/orchestrator/local.go` (just `NoopScaler`) — inline or delete once the Scaler interface is removed.
- `internal/orchestrator/scaler.go` — whole interface goes; dispatch no longer calls `RequestScaleUp`.
- Spot-interruption handling (`spot.go`) — gone with EC2.

`go.mod` dependencies to remove with `go mod tidy`:

```
github.com/aws/aws-sdk-go-v2
github.com/aws/aws-sdk-go-v2/config
github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs
github.com/aws/aws-sdk-go-v2/service/ec2
github.com/aws/aws-sdk-go-v2/service/ecs
github.com/aws/aws-sdk-go-v2/service/s3
github.com/aws/aws-sdk-go-v2/service/ssm
```

## Simplify

### `internal/orchestrator/docker/command.go`

Delete `runSSMCommand`, delete the mode switch at line 19, keep only `runLocalCommand`. `runCommand` becomes a thin wrapper around `exec.CommandContext`.

### `internal/orchestrator/docker/docker.go`

Drop any `if cfg.Mode == ModeLocal` branches — the env-file-on-host path (lines 38–44, 204) becomes the only path. `InspectContainer`, `StopContainer`, `GetLogs`, `GetAgentOutput` keep the same docker-CLI invocations.

### `internal/orchestrator/orchestrator.go`

Collapse `initLocalMode` / `initEC2Mode` / `initFargateMode` into a single `initInstance` that upserts one synthetic `"local"` instance row (matches today's `local.go` behaviour). Remove the spot-handler call in `tick()` at line 213. Keep the synthetic `instances` row — dispatch's capacity accounting still routes through it, and it's the least-invasive way to preserve `IncrementRunningContainers` / `DecrementRunningContainers`.

### `internal/orchestrator/dispatch.go`

Remove the `Scaler.RequestScaleUp` call (lines 73–77). When capacity is exhausted, dispatch simply leaves the task `pending` for the next tick. That matches today's local-mode behaviour.

### `internal/orchestrator/runner.go`

`Runner` interface stays — only one impl (`docker.Manager`). Drop `S3Client` interface and the `o.s3` field.

### `cmd/backflow/main.go`

Replace lines 123–148 with:

```go
runner := orchdocker.NewManager(cfg, db)
fsOutputs := outputs.New(cfg.DataDir)           // new package
orch := orchestrator.New(db, runner, fsOutputs, cfg, bus)
```

No AWS config loader, no `orchs3.NewUploader`, no `orchec2.NewScaler`, no `orchec2.NewSpotHandler`, no `orchfargate.NewManager`.

## Replace: S3 → filesystem

Agent output upload today: `internal/orchestrator/monitor.go:266–290` calls `o.s3.Upload(ctx, key, data)` with `key = tasks/{id}/container_output.log`, then persists the returned URL via `CompleteTask(OutputURL=...)`.

New package `internal/orchestrator/outputs/` (small, ~80 LoC):

- `type FSWriter struct { root string }`
- `Save(ctx, taskID string, logBytes []byte, metadata any) (localURL string, err error)` — writes:
  - `{root}/tasks/{taskID}/container_output.log`
  - `{root}/tasks/{taskID}/task.json` (marshalled `*models.Task` snapshot — this is the "task metadata" piece)
  - Atomic: write to `*.tmp` then `os.Rename`.
  - Returns `file:///absolute/path` or a relative API URL; see below.

New API endpoint: `GET /tasks/{id}/output` and `GET /tasks/{id}/output.json` in `internal/api/handlers.go`. Serves the files via `http.ServeFile` with the same bearer-auth guard as the rest of `/api/v1/*`. `Task.OutputURL` is populated with `/api/v1/tasks/{id}/output` so existing webhook payloads keep a usable link.

Monitor.go change is localised to `saveAgentOutput` — swap `o.s3.Upload` / `UploadJSON` calls for `o.outputs.Save`. The `task.SaveAgentOutput` gate stays.

Config: new env var `BACKFLOW_DATA_DIR` (default `./data`). Document in `CLAUDE.md`.

## Config (`internal/config/config.go`)

Remove fields and env vars:

- `Mode`, `BACKFLOW_MODE`
- `AWSRegion`, `AWS_REGION`
- All `BACKFLOW_ECS_*`, `BACKFLOW_CLOUDWATCH_LOG_GROUP`, `BACKFLOW_MAX_CONCURRENT_TASKS` (Fargate)
- `BACKFLOW_INSTANCE_TYPE`, `BACKFLOW_LAUNCH_TEMPLATE_ID`, `BACKFLOW_AMI`, `BACKFLOW_MAX_INSTANCES`
- `BACKFLOW_S3_BUCKET`
- `BACKFLOW_RESTRICT_API` (Fly-only guard)
- All `BACKFLOW_DISCORD_*`
- All Twilio / messaging env vars

Add:

- `DataDir` / `BACKFLOW_DATA_DIR`

Raise the `ContainersPerInstance` max cap (currently 6 for local mode at `config.go:219`) — still fine as 6, but make it the only cap.

`TaskDefaults("read")` keeps working since reader mode stays. `BACKFLOW_READER_IMAGE` stays.

## Store (`internal/store/`)

The Postgres implementation stays as-is. Only **remove** interface methods and migrations that are now orphaned:

Drop from `Store`:
- `GetAllowedSender`, `CreateAllowedSender`
- `UpsertDiscordInstall`, `GetDiscordInstall`, `DeleteDiscordInstall`
- `UpsertDiscordTaskThread`, `GetDiscordTaskThread`

New migration `013_backflow_lite_cleanup.sql`:

```sql
-- +goose Up
DROP TABLE IF EXISTS discord_task_threads;
DROP TABLE IF EXISTS discord_installs;
DROP TABLE IF EXISTS allowed_senders;
-- +goose Down
-- (no-op; reinstalling mainline re-creates via earlier migrations)
```

Keep `api_keys`, `tasks`, `instances`, `readings`.

## API (`internal/api/`)

Remove handlers:

- `POST /webhooks/discord`
- `POST /webhooks/sms/inbound`

Keep handlers: `/health`, `/api/v1/health`, `/tasks`, `/tasks/{id}`, `/tasks/{id}/logs`, `/tasks/{id}/retry`, `/debug/stats`. Add `/tasks/{id}/output` and `/tasks/{id}/output.json` (see above).

`internal/api/NewTask`, `CancelTask`, `RetryTask` helpers stay — they're reused by whatever frontend sits in front (Discord is gone but future UIs can still share them).

## Notifier (`internal/notify/`)

Keep: `Notifier` interface, `EventBus`, `NewEvent`, `WithReading`, `WebhookNotifier`, `NoopNotifier`. Drop `DiscordNotifier`, `MessagingNotifier`. `Event.TaskMode` and reading fields stay — webhook payload is unchanged.

## Makefile

Delete targets: `setup-aws`, `docker-*-push`, `docker-*-deploy`, `cloudflared-setup`, `tunnel`, `deploy-site`, `restore-env`, `backup-env`, `test-blackbox` keeps (runs local Docker + local Postgres), `test-soak` keeps.

Simplify `run`: drop the `aws sts get-caller-identity` / `aws login` preflight (lines 23–30).

## Tests

- `internal/orchestrator/*_test.go` — delete EC2/Fargate/mode-switch tests (`TestInitEC2Mode_*`, fargate tests). Keep local-mode and recovery tests.
- `test/blackbox/harness_test.go:195` already sets `BACKFLOW_MODE=local`; remove the env var and let default behaviour stand.
- `test/soak/` — same.
- Add a small test for `outputs.FSWriter.Save` covering atomic write + directory creation.

## File-level checklist

Modified (high-churn):
- `cmd/backflow/main.go` — runner wiring, AWS client deletion
- `internal/config/config.go` — env var pruning
- `internal/orchestrator/orchestrator.go` — collapse mode init, drop spot handler
- `internal/orchestrator/dispatch.go` — drop Scaler call
- `internal/orchestrator/monitor.go:266–290` — S3 → filesystem
- `internal/orchestrator/docker/docker.go`, `command.go` — drop SSM path
- `internal/api/router.go` / `handlers.go` — drop Discord/SMS routes, add output endpoints
- `internal/store/store.go` + `postgres.go` — drop orphan methods
- `Makefile`, `go.mod`, `go.sum`

Deleted (whole files/dirs):
- `internal/orchestrator/{ec2,fargate,s3,local}.go?` and subdirs
- `internal/orchestrator/scaler.go`
- `internal/discord/`, `internal/messaging/`
- `internal/notify/discord.go`, `internal/notify/messaging.go`
- `cmd/migrate-to-postgres/`
- `fly.toml`, `docs/fly-setup.md`
- Migrations 002, 003, 005 *(leave in place for upgraders; new migration 013 drops the tables)*

New:
- `internal/orchestrator/outputs/fs.go` (+test)
- `migrations/013_backflow_lite_cleanup.sql`
- `docs/self-hosting.md` (Docker + Supabase setup, replaces Fly doc)

## Verification

1. `make build` — confirm zero AWS SDK imports: `go list -deps ./... | grep aws-sdk-go-v2` must be empty.
2. `make lint && make test` — all unit tests green.
3. `goose -dir migrations up` against a fresh Supabase project — migrations apply cleanly including 013.
4. `BACKFLOW_DATA_DIR=./data make run` — server starts, `/health` OK.
5. `curl -X POST /tasks …` with a simple code task → poll `/tasks/{id}` until `completed`. Verify:
   - Container runs under local Docker (`docker ps` during run).
   - `./data/tasks/{id}/container_output.log` exists after completion.
   - `./data/tasks/{id}/task.json` exists.
   - `GET /tasks/{id}/output` returns the log.
   - Webhook receives `task.completed` with `output_url` pointing at the new endpoint.
6. Submit a reader-mode task against a reachable URL, confirm the Supabase `readings` row appears with embedding, and `novelty_verdict` is populated.
7. `make test-blackbox` — passes end-to-end with only local Docker + local Postgres.

## Fork-specific HANDOFF entry

When the implementation branch is opened, add to the fork's `HANDOFF.md`:

- Why AWS/Fly/Discord/SMS were removed and the upstream packages kept for reference.
- That `instances` table and the `Runner` interface survived despite being 1-impl — reintroducing a remote runner later should be additive.
- That migration 013 is destructive and forked deployments are expected to run it once.