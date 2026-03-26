#!/usr/bin/env bash
# Run the Backflow black-box integration test suite.
set -euo pipefail

usage() {
    cat <<EOF
Usage: $(basename "$0") [options]

Run the Backflow black-box integration test suite.

Builds the Backflow binary and fake agent Docker image, starts Postgres via
testcontainers, and exercises the full task lifecycle.

Prerequisites: Docker daemon must be running.

Options:
  -h, --help    Show this help message

Any additional flags are passed through to 'go test'.

Examples:
  $(basename "$0")                  # Run all black-box tests
  $(basename "$0") -run TestHappy   # Run a specific test
EOF
}

case "${1:-}" in
    -h|--help) usage; exit 0 ;;
esac

if ! docker info >/dev/null 2>&1; then
    echo "error: Docker daemon is not running" >&2
    exit 1
fi

exec go test ./test/blackbox/ -v -count=1 -timeout 120s "$@"
