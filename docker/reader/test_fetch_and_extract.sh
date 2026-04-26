#!/usr/bin/env bash
set -euo pipefail

# Tests for fetch-and-extract.sh. Hermetic: spins up a local python3 http
# server on a random port, points the script at it, and stubs the Node
# extractor with a small bash command. Real Readability extraction is
# verified manually in the docker image e2e demo.

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$DIR/fetch-and-extract.sh"

if [ ! -x "$SCRIPT" ]; then
    echo "FAIL: $SCRIPT is missing or not executable" >&2
    exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
    echo "SKIP: python3 not available" >&2
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
SERVER_DIR="$TMPROOT/serve"
WORKSPACE="$TMPROOT/workspace"
mkdir -p "$SERVER_DIR" "$WORKSPACE"

cat > "$SERVER_DIR/index.html" <<'HTML'
<!DOCTYPE html>
<html>
  <head><title>Hermetic Test Page</title></head>
  <body><h1>Hello</h1><p>This is fixture HTML used by the fetcher test.</p></body>
</html>
HTML

# Pick a free port. If multiple test workers run we'll just retry once.
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
( cd "$SERVER_DIR" && python3 -m http.server "$PORT" --bind 127.0.0.1 ) >/dev/null 2>&1 &
SERVER_PID=$!

# Wait for the server to come up (max ~2 seconds).
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if curl -sf "http://127.0.0.1:$PORT/index.html" -o /dev/null 2>/dev/null; then
        break
    fi
    sleep 0.1
done

# Stub the Node extractor: copy raw bytes verbatim so the test can assert a
# non-empty extracted.md exists without requiring jsdom/readability/turndown.
EXTRACT_CMD_STUB="$TMPROOT/extract-stub.sh"
cat > "$EXTRACT_CMD_STUB" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
# Args: <input.html> <output.md>
cp "$1" "$2"
STUB
chmod +x "$EXTRACT_CMD_STUB"

# --- Run the script ---
URL="http://127.0.0.1:$PORT/index.html" \
WORKSPACE="$WORKSPACE" \
EXTRACT_CMD="$EXTRACT_CMD_STUB" \
    bash "$SCRIPT" || fail "fetch-and-extract.sh exited non-zero"

# --- Assertions ---
[ -f "$WORKSPACE/raw.html" ] || fail "raw.html missing"
[ -f "$WORKSPACE/extracted.md" ] || fail "extracted.md missing"
[ -f "$WORKSPACE/content.json" ] || fail "content.json missing"

if ! grep -q "Hermetic Test Page" "$WORKSPACE/raw.html"; then
    fail "raw.html does not contain expected fixture content"
fi

if ! grep -q "Hermetic Test Page" "$WORKSPACE/extracted.md"; then
    fail "extracted.md (stub copy) does not contain expected content"
fi

# Sidecar must be parseable JSON with the expected fields.
sidecar="$WORKSPACE/content.json"
if ! jq -e '.content_status == "captured"' "$sidecar" >/dev/null; then
    fail "content.json: content_status != captured ($(cat "$sidecar"))"
fi
if ! jq -e '.url == "http://127.0.0.1:'"$PORT"'/index.html"' "$sidecar" >/dev/null; then
    fail "content.json: url mismatch ($(cat "$sidecar"))"
fi
if ! jq -e '.content_type | test("html"; "i")' "$sidecar" >/dev/null; then
    fail "content.json: content_type does not match html ($(cat "$sidecar"))"
fi
if ! jq -e '.content_bytes > 0' "$sidecar" >/dev/null; then
    fail "content.json: content_bytes not positive ($(cat "$sidecar"))"
fi
if ! jq -e '.extracted_bytes > 0' "$sidecar" >/dev/null; then
    fail "content.json: extracted_bytes not positive ($(cat "$sidecar"))"
fi
if ! jq -e '.content_sha256 | test("^[0-9a-f]{64}$")' "$sidecar" >/dev/null; then
    fail "content.json: content_sha256 not a hex sha256 ($(cat "$sidecar"))"
fi
if ! jq -e '.fetched_at | test("^[0-9]{4}-")' "$sidecar" >/dev/null; then
    fail "content.json: fetched_at not RFC3339-ish ($(cat "$sidecar"))"
fi

# --- Missing URL → non-zero exit ---
unset URL
if WORKSPACE="$TMPROOT/empty" EXTRACT_CMD="$EXTRACT_CMD_STUB" bash "$SCRIPT" >/dev/null 2>&1; then
    fail "expected non-zero exit when URL is empty"
fi

echo "ok"
