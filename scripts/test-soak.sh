#!/usr/bin/env bash
set -euo pipefail

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
