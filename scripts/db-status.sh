#!/usr/bin/env bash
set -euo pipefail

DB="${BACKFLOW_DB_PATH:-backflow.db}"

if [ ! -f "$DB" ]; then
    echo "Database not found: $DB"
    exit 1
fi

echo "=== Tasks ==="
sqlite3 -header -column "$DB" "SELECT * FROM tasks ORDER BY created_at DESC;"

echo ""
echo "=== Task Summary ==="
sqlite3 -header -column "$DB" "
    SELECT status, count(*) as count FROM tasks GROUP BY status;
"

echo ""
echo "=== Instances ==="
sqlite3 -header -column "$DB" "
    SELECT instance_id, instance_type, status, private_ip,
           running_containers || '/' || max_containers as containers,
           created_at, updated_at
    FROM instances ORDER BY created_at DESC;
"

echo ""
echo "=== Instance Summary ==="
sqlite3 -header -column "$DB" "
    SELECT status, count(*) as count FROM instances GROUP BY status;
"
