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

# --- HTTP 404 → fetch_failed sidecar, no raw, no extracted ---
WS_404="$TMPROOT/ws_404"
mkdir -p "$WS_404"
URL="http://127.0.0.1:$PORT/does-not-exist.html" \
WORKSPACE="$WS_404" \
EXTRACT_CMD="$EXTRACT_CMD_STUB" \
    bash "$SCRIPT" || fail "fetch-and-extract.sh must exit 0 on HTTP 404 (capture is non-fatal)"

if [ -e "$WS_404/raw.html" ]; then
    fail "raw.html must not exist on HTTP 404"
fi
if [ -e "$WS_404/extracted.md" ]; then
    fail "extracted.md must not exist on HTTP 404"
fi
if [ ! -f "$WS_404/content.json" ]; then
    fail "content.json must exist on HTTP 404 so the orchestrator can record fetch_failed"
fi
if ! jq -e '.content_status == "fetch_failed"' "$WS_404/content.json" >/dev/null; then
    fail "content.json: content_status != fetch_failed ($(cat "$WS_404/content.json"))"
fi

# --- HTTP 403 → fetch_failed (any non-2xx) ---
# python3 -m http.server only serves 200/404, so simulate 403 via a curl that
# follows redirects to a non-existent host. Skipped since 404 already exercises
# the non-2xx branch — adding a second case is redundant.

# --- Oversized payload + small MAX_CONTENT_BYTES → over_size_cap, no raw ---
# Create a 100KB fixture and set MAX_CONTENT_BYTES=1024 so curl's
# --max-filesize trips. Capture must record over_size_cap and remove raw.
dd if=/dev/zero bs=1024 count=100 2>/dev/null | tr '\0' 'A' > "$SERVER_DIR/big.html"
WS_BIG="$TMPROOT/ws_big"
mkdir -p "$WS_BIG"
URL="http://127.0.0.1:$PORT/big.html" \
WORKSPACE="$WS_BIG" \
EXTRACT_CMD="$EXTRACT_CMD_STUB" \
MAX_CONTENT_BYTES=1024 \
    bash "$SCRIPT" || fail "fetch-and-extract.sh must exit 0 on size cap (capture is non-fatal)"

if [ -e "$WS_BIG/raw.html" ] || [ -e "$WS_BIG/raw.bin" ]; then
    fail "no raw file should exist on over_size_cap"
fi
if [ ! -f "$WS_BIG/content.json" ]; then
    fail "content.json must exist on over_size_cap"
fi
if ! jq -e '.content_status == "over_size_cap"' "$WS_BIG/content.json" >/dev/null; then
    fail "content.json: content_status != over_size_cap ($(cat "$WS_BIG/content.json"))"
fi

# --- Within-cap fetch still succeeds when MAX_CONTENT_BYTES is set generously ---
WS_OK="$TMPROOT/ws_ok"
mkdir -p "$WS_OK"
URL="http://127.0.0.1:$PORT/index.html" \
WORKSPACE="$WS_OK" \
EXTRACT_CMD="$EXTRACT_CMD_STUB" \
MAX_CONTENT_BYTES=1048576 \
    bash "$SCRIPT" || fail "fetch with generous cap must succeed"
if ! jq -e '.content_status == "captured"' "$WS_OK/content.json" >/dev/null; then
    fail "within-cap fetch must remain captured ($(cat "$WS_OK/content.json"))"
fi

# --- Non-HTML payloads: served by a small custom server with content-type
# headers. The python http.server only sets text/html; spin up a one-shot
# alternative server using a here-doc CGI-style script.
NHS_DIR="$TMPROOT/nh_serve"
mkdir -p "$NHS_DIR"
printf '%%PDF-1.4\n%%minimal pdf bytes\n' > "$NHS_DIR/doc.pdf"
printf '{"hello":"world"}\n' > "$NHS_DIR/data.json"
printf 'plain text body\n' > "$NHS_DIR/notes.txt"

# python -m http.server picks Content-Type by extension via mimetypes (which
# already covers .pdf=application/pdf, .json=application/json, .txt=text/plain).
NHS_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
( cd "$NHS_DIR" && python3 -m http.server "$NHS_PORT" --bind 127.0.0.1 ) >/dev/null 2>&1 &
NHS_PID=$!
trap 'kill "${SERVER_PID:-}" 2>/dev/null; kill "${NHS_PID:-}" 2>/dev/null; rm -rf "$TMPROOT"' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    if curl -sf "http://127.0.0.1:$NHS_PORT/doc.pdf" -o /dev/null 2>/dev/null; then
        break
    fi
    sleep 0.1
done

assert_non_html_capture() {
    # Args: <url-path> <ext> <content-type-substr> <workspace-dir>
    local urlpath="$1" ext="$2" ctsubstr="$3" ws="$4"
    mkdir -p "$ws"
    URL="http://127.0.0.1:$NHS_PORT/$urlpath" \
    WORKSPACE="$ws" \
    EXTRACT_CMD="$EXTRACT_CMD_STUB" \
    MAX_CONTENT_BYTES=1048576 \
        bash "$SCRIPT" || fail "non-html fetch ($urlpath) must succeed"
    if [ ! -f "$ws/raw.$ext" ]; then
        fail "raw.$ext must exist for $urlpath (got: $(ls "$ws"))"
    fi
    if [ -e "$ws/raw.html" ]; then
        fail "raw.html must not exist for non-html $urlpath"
    fi
    if [ -e "$ws/extracted.md" ]; then
        fail "extracted.md must not exist for non-html $urlpath"
    fi
    if ! jq -e '.content_status == "captured"' "$ws/content.json" >/dev/null; then
        fail "non-html $urlpath: content_status != captured ($(cat "$ws/content.json"))"
    fi
    if ! jq -e --arg ct "$ctsubstr" '.content_type | test($ct; "i")' "$ws/content.json" >/dev/null; then
        fail "non-html $urlpath: content_type does not match $ctsubstr ($(cat "$ws/content.json"))"
    fi
}

assert_non_html_capture "doc.pdf"   "pdf"  "application/pdf"  "$TMPROOT/ws_pdf"
assert_non_html_capture "data.json" "json" "application/json" "$TMPROOT/ws_json"
assert_non_html_capture "notes.txt" "txt"  "text/plain"       "$TMPROOT/ws_txt"

# --- INLINE_CONTENT_PATH short-circuit ---
INLINE_DIR="$TMPROOT/inline"
INLINE_WS="$TMPROOT/inline-ws"
mkdir -p "$INLINE_DIR" "$INLINE_WS"

INLINE_FILE="$INLINE_DIR/note.md"
cat > "$INLINE_FILE" <<'MARKDOWN'
# Inline Test Note

Body of the inline-ingested markdown file.
MARKDOWN

# Run with INLINE_CONTENT_PATH set, no URL.
unset URL
INLINE_CONTENT_PATH="$INLINE_FILE" \
WORKSPACE="$INLINE_WS" \
EXTRACT_CMD="$EXTRACT_CMD_STUB" \
    bash "$SCRIPT" || fail "fetch-and-extract.sh inline branch exited non-zero"

# extracted.md must be byte-equal to the input file.
if ! cmp -s "$INLINE_FILE" "$INLINE_WS/extracted.md"; then
    fail "extracted.md not byte-equal to INLINE_CONTENT_PATH input"
fi

# raw.html must NOT exist for the inline branch.
if [ -e "$INLINE_WS/raw.html" ]; then
    fail "raw.html should not exist for inline-content branch"
fi

# content.json sidecar — same fields as URL branch but content_type=text/markdown.
inline_sidecar="$INLINE_WS/content.json"
if [ ! -f "$inline_sidecar" ]; then
    fail "content.json missing for inline branch"
fi
if ! jq -e '.content_status == "captured"' "$inline_sidecar" >/dev/null; then
    fail "inline content.json: content_status != captured ($(cat "$inline_sidecar"))"
fi
if ! jq -e '.content_type == "text/markdown"' "$inline_sidecar" >/dev/null; then
    fail "inline content.json: content_type != text/markdown ($(cat "$inline_sidecar"))"
fi
if ! jq -e '.content_bytes > 0' "$inline_sidecar" >/dev/null; then
    fail "inline content.json: content_bytes not positive ($(cat "$inline_sidecar"))"
fi
if ! jq -e '.extracted_bytes > 0' "$inline_sidecar" >/dev/null; then
    fail "inline content.json: extracted_bytes not positive ($(cat "$inline_sidecar"))"
fi
if ! jq -e '.content_sha256 | test("^[0-9a-f]{64}$")' "$inline_sidecar" >/dev/null; then
    fail "inline content.json: content_sha256 not a hex sha256 ($(cat "$inline_sidecar"))"
fi
if ! jq -e '.fetched_at | test("^[0-9]{4}-")' "$inline_sidecar" >/dev/null; then
    fail "inline content.json: fetched_at not RFC3339-ish ($(cat "$inline_sidecar"))"
fi

echo "ok"
