#!/usr/bin/env bash
# Run schemathesis fuzz tests against the OpenAPI spec locally.
# Starts a temporary Postgres, builds and runs the server, then fuzzes.
set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [options]

Run schemathesis fuzz tests against the Backflow OpenAPI spec.

Starts a temporary Postgres container, builds and runs the server, then fuzzes
all endpoints using schemathesis.

Prerequisites: Docker daemon must be running.

Environment variables:
  MAX_EXAMPLES   Number of test examples per phase (default: 20)
  SEED           Random seed for reproducibility (default: 42)

Options:
  -h, --help    Show this help message

Examples:
  $(basename "$0")                    # Run with defaults
  MAX_EXAMPLES=50 $(basename "$0")    # More thorough fuzzing
EOF
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
esac

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PG_CONTAINER="backflow-schema-test-pg"
PG_PORT=5433
DB_URL="postgres://backflow:backflow@localhost:${PG_PORT}/backflow?sslmode=disable"
SERVER_PID=""

cleanup() {
    echo ""
    echo "Cleaning up..."
    [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null && wait "$SERVER_PID" 2>/dev/null || true
    docker rm -f "$PG_CONTAINER" 2>/dev/null || true
}
trap cleanup EXIT

# --- Check prerequisites ---
if ! command -v docker &>/dev/null; then
    echo "error: docker not found" >&2
    exit 1
fi

GOOSE="go run github.com/pressly/goose/v3/cmd/goose@latest"

VENV_DIR="$ROOT_DIR/.cache/schemathesis-venv"
if ! command -v schemathesis &>/dev/null; then
    if [ ! -x "$VENV_DIR/bin/schemathesis" ]; then
        echo "Installing schemathesis into $VENV_DIR..."
        python3 -m venv "$VENV_DIR"
        "$VENV_DIR/bin/pip" install --quiet schemathesis
    fi
fi
SCHEMATHESIS="${SCHEMATHESIS:-$(command -v schemathesis 2>/dev/null || echo "$VENV_DIR/bin/schemathesis")}"

# --- Start Postgres ---
echo "Starting temporary Postgres on port ${PG_PORT}..."
docker rm -f "$PG_CONTAINER" 2>/dev/null || true
docker run -d --name "$PG_CONTAINER" \
    -e POSTGRES_DB=backflow \
    -e POSTGRES_USER=backflow \
    -e POSTGRES_PASSWORD=backflow \
    -p "${PG_PORT}:5432" \
    postgres:16-alpine >/dev/null

echo "Waiting for Postgres..."
for i in $(seq 1 15); do
    if docker exec "$PG_CONTAINER" pg_isready -U backflow -q 2>/dev/null; then
        break
    fi
    sleep 1
done
docker exec "$PG_CONTAINER" pg_isready -U backflow -q || { echo "Postgres did not start"; exit 1; }

# --- Migrate ---
echo "Running migrations..."
$GOOSE -dir "$ROOT_DIR/migrations" postgres "$DB_URL" up

# --- Build & start server ---
echo "Building..."
(cd "$ROOT_DIR" && go build -trimpath -o bin/backflow ./cmd/backflow)

echo "Starting server..."
BACKFLOW_DATABASE_URL="$DB_URL" \
BACKFLOW_MODE=local \
ANTHROPIC_API_KEY=sk-ant-fuzz-placeholder-not-real \
    "$ROOT_DIR/bin/backflow" &
SERVER_PID=$!

echo "Waiting for server..."
for i in $(seq 1 15); do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        echo "Server process died" && exit 1
    fi
    curl -sf http://localhost:8080/health >/dev/null && break || sleep 1
done
curl -sf http://localhost:8080/health >/dev/null || { echo "Server did not start"; exit 1; }

# --- Fuzz ---
echo "Running schemathesis..."
$SCHEMATHESIS run "$ROOT_DIR/api/openapi.yaml" \
    --url http://localhost:8080 \
    --checks not_a_server_error \
    --phases examples,coverage,fuzzing,stateful \
    --suppress-health-check filter_too_much \
    --max-examples "${MAX_EXAMPLES:-20}" \
    --seed "${SEED:-42}"
