#!/usr/bin/env bash
set -euo pipefail

# Backflow Fly.io secrets sync
# Reads KEY=VALUE pairs from .env and pushes the allowlisted subset to the
# Fly app as secrets. Dry-run by default; pass --apply to actually push.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

ENV_FILE="$REPO_ROOT/.env"
APP="backflow"
APPLY=0

usage() {
    cat <<'EOF'
Usage: fly-secrets-sync.sh [--apply] [--app <name>] [--env-file <path>]

Reads KEY=VALUE lines from the env file and pushes the allowlisted keys to
the Fly app as secrets (single batched redeploy). Dry-run by default.

Options:
  --apply              Push to Fly. Without this flag, only prints what would be set.
  --app <name>         Fly app to target (default: backflow).
  --env-file <path>    Env file to read (default: <repo>/.env).
  -h, --help           Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --apply) APPLY=1; shift ;;
        --app) APP="$2"; shift 2 ;;
        --env-file) ENV_FILE="$2"; shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
    esac
done

# Allowlist: vars the Fly server consumes at runtime.
# Excluded (owned by fly.toml [env] or local-only):
#   BACKFLOW_MODE, BACKFLOW_RESTRICT_API, BACKFLOW_TUNNEL_NAME, BACKFLOW_DOMAIN,
#   BACKFLOW_LISTEN_ADDR, BACKFLOW_DB_PATH, BACKFLOW_LOG_FILE, BACKFLOW_AUTH_MODE,
#   BACKFLOW_GITHUB_REPO, BACKFLOW_INSTANCE_TYPE, BACKFLOW_LAUNCH_TEMPLATE_ID,
#   BACKFLOW_MAX_INSTANCES, BACKFLOW_CONTAINERS_PER_INSTANCE, BACKFLOW_AMI.
ALLOWLIST=(
    # Secrets
    ANTHROPIC_API_KEY OPENAI_API_KEY GITHUB_TOKEN BACKFLOW_API_KEY

    # DB + storage
    BACKFLOW_DATABASE_URL BACKFLOW_S3_BUCKET

    # Agent / reader images + defaults
    BACKFLOW_AGENT_IMAGE BACKFLOW_READER_IMAGE
    BACKFLOW_DEFAULT_HARNESS BACKFLOW_DEFAULT_CLAUDE_MODEL BACKFLOW_DEFAULT_CODEX_MODEL
    BACKFLOW_DEFAULT_EFFORT BACKFLOW_DEFAULT_MAX_BUDGET
    BACKFLOW_DEFAULT_MAX_RUNTIME_SEC BACKFLOW_DEFAULT_MAX_TURNS
    BACKFLOW_DEFAULT_READ_MAX_BUDGET BACKFLOW_DEFAULT_READ_MAX_RUNTIME_SEC BACKFLOW_DEFAULT_READ_MAX_TURNS
    BACKFLOW_DEFAULT_CREATE_PR BACKFLOW_DEFAULT_SELF_REVIEW BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT
    BACKFLOW_MAX_USER_RETRIES

    # Supabase (reader)
    SUPABASE_URL SUPABASE_ANON_KEY

    # AWS
    AWS_REGION AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY

    # Fargate / ECS
    BACKFLOW_ECS_CLUSTER BACKFLOW_ECS_TASK_DEFINITION BACKFLOW_ECS_READER_TASK_DEFINITION
    BACKFLOW_ECS_SUBNETS BACKFLOW_ECS_SECURITY_GROUPS
    BACKFLOW_CLOUDWATCH_LOG_GROUP

    # Webhook
    BACKFLOW_WEBHOOK_URL BACKFLOW_WEBHOOK_EVENTS
)

if [[ ! -f "$ENV_FILE" ]]; then
    echo "error: env file not found: $ENV_FILE" >&2
    exit 1
fi

if [[ $APPLY -eq 1 ]] && ! command -v fly >/dev/null 2>&1; then
    echo "error: fly CLI not found. Install from https://fly.io/docs/flyctl/install/" >&2
    exit 1
fi

# Space-delimited strings for membership checks (bash 3.2 has no associative arrays).
ALLOW_STR=" ${ALLOWLIST[*]} "
SEEN_STR=" "

# Parse env file. Collect KEY=VALUE in two parallel arrays (preserves order).
KEYS=()
VALUES=()
SKIPPED=()

while IFS= read -r raw || [[ -n "$raw" ]]; do
    # Strip trailing CR (in case of CRLF files).
    raw="${raw%$'\r'}"
    # Trim leading whitespace.
    line="${raw#"${raw%%[![:space:]]*}"}"
    # Skip blank and comment lines.
    [[ -z "$line" || "${line:0:1}" == "#" ]] && continue
    # Accept optional `export ` prefix.
    [[ "$line" == export\ * ]] && line="${line#export }"
    # Must look like KEY=VALUE with POSIX-ish key.
    if [[ ! "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
        continue
    fi
    key="${BASH_REMATCH[1]}"
    val="${BASH_REMATCH[2]}"
    # Strip matching surrounding quotes (single or double).
    if [[ ${#val} -ge 2 ]]; then
        first="${val:0:1}"; last="${val: -1}"
        if [[ ( "$first" == '"' && "$last" == '"' ) || ( "$first" == "'" && "$last" == "'" ) ]]; then
            val="${val:1:${#val}-2}"
        fi
    fi

    if [[ "$ALLOW_STR" == *" $key "* ]]; then
        if [[ "$SEEN_STR" == *" $key "* ]]; then
            # Later wins (matches `source`'d env behavior). Find & overwrite.
            for i in "${!KEYS[@]}"; do
                if [[ "${KEYS[$i]}" == "$key" ]]; then
                    VALUES[$i]="$val"
                    break
                fi
            done
        else
            KEYS+=("$key")
            VALUES+=("$val")
            SEEN_STR="$SEEN_STR$key "
        fi
    else
        SKIPPED+=("$key")
    fi
done < "$ENV_FILE"

if [[ ${#KEYS[@]} -eq 0 ]]; then
    echo "error: no allowlisted vars found in $ENV_FILE" >&2
    exit 1
fi

is_sensitive() {
    case "$1" in
        *KEY*|*TOKEN*|*SECRET*|*PASSWORD*|*DATABASE_URL*) return 0 ;;
        *) return 1 ;;
    esac
}

mask() {
    local v="$1" n=${#1}
    if [[ $n -le 4 ]]; then
        printf '•••• (%d chars)' "$n"
    else
        printf '%s•••• (%d chars)' "${v:0:4}" "$n"
    fi
}

# Find longest key for column alignment.
maxlen=0
for k in "${KEYS[@]}"; do
    (( ${#k} > maxlen )) && maxlen=${#k}
done

mode="DRY RUN"
[[ $APPLY -eq 1 ]] && mode="APPLY"

echo "==> $mode — env: $ENV_FILE, app: $APP"
echo "==> ${#KEYS[@]} vars to sync:"
for i in "${!KEYS[@]}"; do
    k="${KEYS[$i]}"
    v="${VALUES[$i]}"
    if is_sensitive "$k"; then
        printf '    %-*s  %s\n' "$maxlen" "$k" "$(mask "$v")"
    else
        printf '    %-*s  %s\n' "$maxlen" "$k" "$v"
    fi
done

# Notes: which allowlisted keys are absent from .env (informational).
MISSING=()
for k in "${ALLOWLIST[@]}"; do
    [[ "$SEEN_STR" != *" $k "* ]] && MISSING+=("$k")
done
if [[ ${#MISSING[@]} -gt 0 ]]; then
    echo
    echo "==> Allowlisted but absent from env file (left untouched on Fly):"
    for k in "${MISSING[@]}"; do
        echo "    $k"
    done
fi

if [[ ${#SKIPPED[@]} -gt 0 ]]; then
    echo
    echo "==> Present in env file but not allowlisted (not synced):"
    for k in "${SKIPPED[@]}"; do
        echo "    $k"
    done
fi

# Reader-mode coherence check.
reader_warn=()
if [[ "$SEEN_STR" == *" BACKFLOW_READER_IMAGE "* ]]; then
    for dep in SUPABASE_URL SUPABASE_ANON_KEY BACKFLOW_ECS_READER_TASK_DEFINITION; do
        [[ "$SEEN_STR" != *" $dep "* ]] && reader_warn+=("$dep")
    done
fi
if [[ ${#reader_warn[@]} -gt 0 ]]; then
    echo
    echo "==> WARNING: BACKFLOW_READER_IMAGE is set but these are missing from env:" >&2
    for k in "${reader_warn[@]}"; do
        echo "    $k" >&2
    done
    echo "    Reader-mode tasks will fail to start until these are set." >&2
fi

if [[ $APPLY -eq 0 ]]; then
    echo
    echo "==> Dry run. Re-run with --apply to push to Fly (triggers one redeploy)."
    exit 0
fi

echo
echo "==> Piping to: fly secrets import -a $APP"
{
    for i in "${!KEYS[@]}"; do
        printf '%s=%s\n' "${KEYS[$i]}" "${VALUES[$i]}"
    done
} | fly secrets import -a "$APP"

echo "==> Done. Watch: fly logs -a $APP"
