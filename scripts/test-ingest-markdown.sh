#!/usr/bin/env bash
set -euo pipefail

# Hermetic smoke test for scripts/ingest-markdown.sh. Spins up a tiny python3
# HTTP server that records POST bodies into a file and replies 201 with a JSON
# envelope mimicking the Backlite API. Asserts that the script:
#   - refuses missing path arg
#   - refuses a directory
#   - refuses an empty file
#   - on a valid file, POSTs JSON whose inline_content equals the file body
#     and whose task_mode == "read"

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$DIR/ingest-markdown.sh"

if [ ! -x "$SCRIPT" ]; then
    echo "FAIL: $SCRIPT is missing or not executable" >&2
    exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 not available" >&2
    exit 0
fi

if ! command -v jq >/dev/null 2>&1; then
    echo "SKIP: jq not available" >&2
    exit 0
fi

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

cleanup() {
    if [ -n "${SERVER_PID:-}" ]; then
        kill "$SERVER_PID" 2>/dev/null || true
        wait "$SERVER_PID" 2>/dev/null || true
    fi
    if [ -n "${TMPROOT:-}" ] && [ -d "$TMPROOT" ]; then
        rm -rf "$TMPROOT"
    fi
}
trap cleanup EXIT

TMPROOT="$(mktemp -d)"
CAPTURE_PATH="$TMPROOT/last-post.json"
SERVER_LOG="$TMPROOT/server.log"

# Pick a free port.
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')

# Start a minimal capture server: writes any POST body to CAPTURE_PATH and
# replies with 201 and a Backlite-style envelope.
python3 - "$PORT" "$CAPTURE_PATH" >"$SERVER_LOG" 2>&1 <<'PY' &
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

port = int(sys.argv[1])
capture_path = sys.argv[2]

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        with open(capture_path, "wb") as f:
            f.write(body)
        resp = json.dumps({"data": {"id": "bf_TESTSCRIPT", "status": "pending"}}).encode()
        self.send_response(201)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)

    def log_message(self, *_):  # silence default logging
        return

HTTPServer(("127.0.0.1", port), Handler).serve_forever()
PY
SERVER_PID=$!

# Wait for the server to come up.
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if curl -sf -X POST -d '{}' "http://127.0.0.1:$PORT/" -o /dev/null 2>/dev/null; then
        break
    fi
    sleep 0.1
done

BACKFLOW_URL_TEST="http://127.0.0.1:$PORT"

# 1) missing arg → non-zero
if BACKFLOW_URL="$BACKFLOW_URL_TEST" bash "$SCRIPT" >/dev/null 2>&1; then
    fail "expected non-zero exit when no path arg is given"
fi

# 2) directory arg → non-zero
DIR_ARG="$TMPROOT/somedir"
mkdir -p "$DIR_ARG"
if BACKFLOW_URL="$BACKFLOW_URL_TEST" bash "$SCRIPT" "$DIR_ARG" >/dev/null 2>&1; then
    fail "expected non-zero exit when path is a directory"
fi

# 3) empty file → non-zero
EMPTY_FILE="$TMPROOT/empty.md"
: > "$EMPTY_FILE"
if BACKFLOW_URL="$BACKFLOW_URL_TEST" bash "$SCRIPT" "$EMPTY_FILE" >/dev/null 2>&1; then
    fail "expected non-zero exit when file is empty"
fi

# 4) valid file → POSTs JSON containing inline_content and task_mode=read
VALID_FILE="$TMPROOT/note.md"
cat > "$VALID_FILE" <<'MARKDOWN'
# Note Title

Some body content.
MARKDOWN

rm -f "$CAPTURE_PATH"

if ! BACKFLOW_URL="$BACKFLOW_URL_TEST" bash "$SCRIPT" "$VALID_FILE" >/dev/null 2>&1; then
    fail "expected zero exit on valid file (server log: $(cat "$SERVER_LOG"))"
fi

if [ ! -f "$CAPTURE_PATH" ]; then
    fail "server did not capture the POST body"
fi

if ! jq -e '.task_mode == "read"' "$CAPTURE_PATH" >/dev/null; then
    fail "POST body missing task_mode=read: $(cat "$CAPTURE_PATH")"
fi

# inline_content must equal the file body (jq compares strings exactly).
EXPECTED_BODY="$(cat "$VALID_FILE")"
ACTUAL_BODY="$(jq -r '.inline_content' "$CAPTURE_PATH")"
if [ "$EXPECTED_BODY" != "$ACTUAL_BODY" ]; then
    printf 'inline_content mismatch:\n--- expected ---\n%s\n--- actual ---\n%s\n' "$EXPECTED_BODY" "$ACTUAL_BODY" >&2
    fail "POST body inline_content does not match input file"
fi

echo "ok"
