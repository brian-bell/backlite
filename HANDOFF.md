# HANDOFF.md

Ledger of cross-PR tradeoffs. Each entry is a forward-looking constraint or an explicit deferral — not a changelog. If something here stops being relevant, delete it.

## Completion artifact ordering

- **The writer splits log and metadata into separate calls for a reason.** The orchestrator persists `container_output.log` first to obtain `output_url`, then completes the task in SQLite, reloads the finished row, and only then writes `task.json`. Fusing `SaveLog` and `SaveMetadata` back into one call reintroduces the stale "running" snapshot at `/output.json` that motivated the split.

## Retry output gating

- **`/output` and `/output.json` are gated by current-attempt state, not raw file presence.** The API only serves persisted artifacts when the task is terminal and `output_url` is still set for the current attempt; `RetryTask` and `RequeueTask` clear `output_url` on each new attempt. The filesystem path is per-task, not per-attempt. Any future "per-attempt history" UX must add explicit versioning rather than reusing these endpoints.

## Static site removal

- **The repo no longer ships a static Pages site.** `site/` and `make deploy-site` are gone. If public marketing or legal pages are needed again, recreate them intentionally — don't assume an old Pages deploy is still live, because it isn't.

## AWS runtime removal

- **If AWS execution is ever wanted again, rebuild from scratch — don't try to revive it from git history.** The Fargate and EC2 runners were deeply entangled with ECS task overrides, SSM, and spot-interruption handling that the simplified orchestrator no longer models, and `go list -deps ./... | grep aws-sdk-go-v2` is empty. The leftover teardown helper scripts are tracked for eventual deletion in issue #36.
