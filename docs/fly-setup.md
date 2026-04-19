# Fly.io Deployment Setup

## Prerequisites

- [flyctl](https://fly.io/docs/flyctl/install/) installed
- AWS infrastructure created (`make setup-aws`)
- GitHub repo with Actions enabled

## One-time setup

### 1. Create the Fly app

```bash
fly apps create backflow
```

### 2. Set secrets

Sync all required env vars from `.env` using the helper script:

```bash
./scripts/fly-secrets-sync.sh            # dry-run: prints what would be set (values masked)
./scripts/fly-secrets-sync.sh --apply    # push to Fly (triggers one redeploy)
```

The script reads `.env`, filters to an explicit allowlist of keys the Fly server consumes (see the script source for the list), and pipes the result to `fly secrets import`. Keys already defined in `fly.toml`'s `[env]` (`BACKFLOW_MODE`, `BACKFLOW_RESTRICT_API`) and local-only keys (cloudflared, `BACKFLOW_LOG_FILE`, EC2-mode config, etc.) are excluded.

Useful flags: `--env-file <path>` to read a non-default env file, `--app <name>` to target a different Fly app.

### 3. Set AWS credentials

The `backflow-fly` IAM user is created by `make setup-aws`. Generate access keys and set them as Fly secrets:

```bash
aws iam create-access-key --user-name backflow-fly
fly secrets set AWS_ACCESS_KEY_ID="..." AWS_SECRET_ACCESS_KEY="..."
```

### 4. Add FLY_API_TOKEN to GitHub Actions

Generate a Fly deploy token and add it as a GitHub Actions repository secret named `FLY_API_TOKEN`.

```bash
fly tokens create deploy -x 999999h
```

Add the token at: `Settings → Secrets and variables → Actions → New repository secret`

### 5. Deploy

Merge to main — CI runs tests then deploys automatically. Or deploy manually:

```bash
fly deploy --remote-only
```

## Verify

```bash
fly status                                          # machine running
fly logs                                            # no startup errors
curl https://backflow.fly.dev/health                # 200 (root health, always open)
curl https://backflow.fly.dev/api/v1/health         # 403 (API restricted)
curl https://backflow.fly.dev/api/v1/tasks          # 403 (API restricted)
```

## API access restriction

`BACKFLOW_RESTRICT_API=true` is set in `fly.toml`'s `[env]`. This blocks all `/api/v1/*` endpoints with a 403. Webhook paths (`/webhooks/discord`) and the root `/health` are unaffected.

## Configuration

App configuration lives in `fly.toml`. Secrets are managed via `fly secrets`. See `internal/config/config.go` for all supported env vars and their defaults.

## Useful commands

```bash
fly status              # App and machine status
fly logs                # Stream logs
fly ssh console         # SSH into the machine
fly secrets list        # List configured secrets
fly scale memory 512    # Resize memory (if 256MB is insufficient)
```
