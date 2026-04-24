#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [options]

Run the Backlite soak test (long-running resource leak detector).
Exercises cancel, retry, retry limits, and mixed failure modes.

Requires: BACKFLOW_MAX_CONTAINERS >= 4 (multi-step scenarios
need concurrent container slots). Set BACKFLOW_AGENT_IMAGE=backlite-fake-agent.

Starts a dedicated Backlite subprocess against the soak SQLite database.
WARNING: This will DELETE task data from that soak database.

Options:
  --short              Run a short soak test (10 minutes)
  --duration <dur>     Custom duration (e.g., 30m, 2h)
  --task-interval <d>  Interval between task submissions (default: 30s)
  -h, --help           Show this help message

Examples:
  $(basename "$0") --short         # 10-minute soak test
  $(basename "$0") --duration 30m  # 30-minute soak test
EOF
}

default_soak_db_path() {
    local base="${1:-}"
    if [ -z "$base" ]; then
        base="./backlite.db"
    fi
    case "$base" in
        *-soak.db) printf '%s\n' "$base" ;;
        *.db) printf '%s-soak.db\n' "${base%.db}" ;;
        *) printf '%s-soak.db\n' "$base" ;;
    esac
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
esac

# Source .env if present.
if [ -f .env ]; then
    set -a; . ./.env; set +a
fi

cat <<'WARNING'
WARNING: The soak test starts a dedicated Backlite subprocess against the soak SQLite database.
That database will be truncated before and after the run.
All existing soak task records will be deleted.
WARNING

SOAK_DATABASE_PATH="$(default_soak_db_path "${BACKFLOW_DATABASE_PATH:-}")"

if [ -n "${SOAK_DATABASE_PATH}" ]; then
    echo "  Database: ${SOAK_DATABASE_PATH}"
fi
echo ""

read -rp "Continue? [y/N] " answer
case "$answer" in
    [yY]|[yY][eE][sS]) ;;
    *) echo "Aborted."; exit 0 ;;
esac

echo ""
exec go run ./test/soak/ --database-path "${SOAK_DATABASE_PATH}" "$@"
