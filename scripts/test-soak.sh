#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [options]

Run the Backflow soak test (long-running resource leak detector).
Exercises cancel, retry, retry limits, and mixed failure modes.

Requires: BACKFLOW_CONTAINERS_PER_INSTANCE >= 4 (multi-step scenarios
need concurrent container slots). Set BACKFLOW_AGENT_IMAGE=backflow-fake-agent.

WARNING: This will TRUNCATE the tasks table in your database.

Options:
  --short              Run a short soak test (10 minutes)
  --duration <dur>     Custom duration (e.g., 30m, 2h)
  --task-interval <d>  Interval between task submissions (default: 30s)
  --api-url <url>      Backflow API base URL (default: http://localhost:8080)
  -h, --help           Show this help message

Examples:
  $(basename "$0") --short         # 10-minute soak test
  $(basename "$0") --duration 30m  # 30-minute soak test
EOF
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
esac

# Source .env if present.
if [ -f .env ]; then
    set -a; . ./.env; set +a
fi

cat <<'WARNING'
WARNING: The soak test will TRUNCATE the tasks table in your database.
All existing task records will be deleted.
WARNING

if [ -n "${BACKFLOW_DATABASE_URL:-}" ]; then
    # Show just the host/db portion, not credentials.
    echo "  Database: ${BACKFLOW_DATABASE_URL%%\?*}" | sed 's|://[^@]*@|://***@|'
fi
echo ""

read -rp "Continue? [y/N] " answer
case "$answer" in
    [yY]|[yY][eE][sS]) ;;
    *) echo "Aborted."; exit 0 ;;
esac

echo ""
exec go run ./test/soak/ "$@"
