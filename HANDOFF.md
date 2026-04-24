# HANDOFF.md

Ledger of cross-PR tradeoffs. Each entry is a forward-looking constraint or an explicit deferral — not a changelog. If something here stops being relevant, delete it.

## Static site removal

- **The repo no longer ships a static Pages site.** `site/` and `make deploy-site` are gone. If public marketing or legal pages are needed again, recreate them intentionally — don't assume an old Pages deploy is still live, because it isn't.

## AWS runtime removal

- **If AWS execution is ever wanted again, rebuild from scratch — don't try to revive it from git history.** The Fargate and EC2 runners were deeply entangled with ECS task overrides, SSM, and spot-interruption handling that the simplified orchestrator no longer models, and `go list -deps ./... | grep aws-sdk-go-v2` is empty. The leftover teardown helper scripts are tracked for eventual deletion in issue #36.
