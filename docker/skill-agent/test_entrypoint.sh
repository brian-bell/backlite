#!/usr/bin/env bash
set -euo pipefail

# Tests for the skill-agent entrypoint. Validates pre-flight env, the
# claude_code-only constraint, missing-skill handling, and the cost
# notarization + fallback-status-synth post-processing.
#
# Each test stubs `claude` with a bash script so we can drive the harness
# behaviour end-to-end without launching an LLM.

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENTRYPOINT="$DIR/entrypoint.sh"

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

pass() {
    echo "PASS: $1"
}

# --- Missing PROMPT ---
(
    unset PROMPT TASK_ID
    if output=$("$ENTRYPOINT" 2>&1); then
        fail "entrypoint with no PROMPT should exit non-zero"
    fi
    if [[ "$output" != *"PROMPT"* ]]; then
        fail "missing PROMPT: error should mention PROMPT, got: $output"
    fi
)
pass "rejects missing PROMPT"

# --- Missing TASK_ID ---
(
    export PROMPT="do something"
    unset TASK_ID
    if output=$("$ENTRYPOINT" 2>&1); then
        fail "entrypoint with no TASK_ID should exit non-zero"
    fi
    if [[ "$output" != *"TASK_ID"* ]]; then
        fail "missing TASK_ID: error should mention TASK_ID, got: $output"
    fi
)
pass "rejects missing TASK_ID"

# --- Codex harness rejected ---
(
    export PROMPT="do something"
    export TASK_ID="bf_test"
    export HARNESS=codex
    if output=$("$ENTRYPOINT" 2>&1); then
        fail "entrypoint with codex harness should exit non-zero"
    fi
    if [[ "$output" != *"claude_code"* ]]; then
        fail "codex rejection: error should mention claude_code, got: $output"
    fi
)
pass "rejects codex harness"

# --- Missing ANTHROPIC_API_KEY ---
(
    export PROMPT="do something"
    export TASK_ID="bf_test"
    unset ANTHROPIC_API_KEY
    if output=$("$ENTRYPOINT" 2>&1); then
        fail "entrypoint without ANTHROPIC_API_KEY should exit non-zero"
    fi
    if [[ "$output" != *"ANTHROPIC_API_KEY"* ]]; then
        fail "missing key: error should mention ANTHROPIC_API_KEY, got: $output"
    fi
)
pass "rejects missing ANTHROPIC_API_KEY"

# --- Unknown task_mode (no skill bundle) ---
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude"
    # Stub claude so the harness step would otherwise succeed if reached.
    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"

    export PROMPT="do something"
    export TASK_ID="bf_test"
    export TASK_MODE="bogus"
    export ANTHROPIC_API_KEY="sk-test"

    if output=$("$ENTRYPOINT" 2>&1); then
        fail "entrypoint with unknown task_mode should exit non-zero"
    fi
    if [[ "$output" != *"bogus"* ]]; then
        fail "unknown skill: error should mention the bad task_mode, got: $output"
    fi
)
pass "rejects unknown task_mode (no skill bundle)"

# --- Happy path: claude stub writes valid status.json; entrypoint notarizes cost_usd ---
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude"

    # Stub claude that:
    #   - emits a stream-json result line with total_cost_usd=1.23
    #   - writes a minimal valid status.json into the workspace
    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
mkdir -p "$HOME/workspace"
cat >"$HOME/workspace/status.json" <<JSON
{"complete": true, "needs_input": false, "task_mode": "code", "pr_url": "https://github.com/o/r/pull/1"}
JSON
echo '{"type":"result","total_cost_usd":1.23,"result":"ok"}'
exit 0
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"
    export BACKFLOW_SKILLS_DIR="$DIR/skills"

    export PROMPT="do something"
    export TASK_ID="bf_test"
    export TASK_MODE="code"
    export ANTHROPIC_API_KEY="sk-test"

    if ! "$ENTRYPOINT" >/dev/null 2>&1; then
        fail "entrypoint happy path: expected exit 0"
    fi
    status_json="$tmp/workspace/status.json"
    if [ ! -f "$status_json" ]; then
        fail "expected status.json at $status_json"
    fi
    cost=$(jq -r '.cost_usd' "$status_json")
    if [ "$cost" != "1.23" ]; then
        fail "cost_usd not notarized: got $cost from $(cat "$status_json")"
    fi
)
pass "notarizes cost_usd into agent-written status.json"

# --- Fallback synth when agent doesn't write status.json ---
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude"

    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
echo "claude crashed without writing status"
exit 5
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"
    export BACKFLOW_SKILLS_DIR="$DIR/skills"

    export PROMPT="do something"
    export TASK_ID="bf_test"
    export TASK_MODE="code"
    export ANTHROPIC_API_KEY="sk-test"

    set +e
    "$ENTRYPOINT" >/dev/null 2>&1
    rc=$?
    set -e
    if [ $rc -eq 0 ]; then
        fail "entrypoint should exit non-zero when claude crashes without status.json"
    fi
    status_json="$tmp/workspace/status.json"
    if [ ! -f "$status_json" ]; then
        fail "fallback synth: expected status.json to be created at $status_json"
    fi
    if [ "$(jq -r '.complete' "$status_json")" != "false" ]; then
        fail "fallback synth: status.complete should be false; got $(cat "$status_json")"
    fi
    if [ "$(jq -r '.exit_code' "$status_json")" != "5" ]; then
        fail "fallback synth: exit_code should be 5; got $(cat "$status_json")"
    fi
)
pass "synthesizes fallback failure status when agent crashes"

# --- Skill install side effect: bundle copied to ~/.claude/skills/${TASK_MODE} ---
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude"

    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
mkdir -p "$HOME/workspace"
cat >"$HOME/workspace/status.json" <<JSON
{"complete": true, "needs_input": false, "task_mode": "code"}
JSON
exit 0
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"
    export BACKFLOW_SKILLS_DIR="$DIR/skills"

    export PROMPT="do something"
    export TASK_ID="bf_test"
    export TASK_MODE="code"
    export ANTHROPIC_API_KEY="sk-test"

    "$ENTRYPOINT" >/dev/null 2>&1 || true
    if [ ! -f "$tmp/.claude/skills/code/SKILL.md" ]; then
        fail "skill install: expected ~/.claude/skills/code/SKILL.md to exist"
    fi
)
pass "installs requested skill bundle into ~/.claude/skills/<mode>/"

# --- Auto mode: succeeds and installs code+review sub-bundles for dispatch ---
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude"

    # Stub claude that pretends the auto skill picked code: writes a valid
    # status.json with the resolved concrete mode.
    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
mkdir -p "$HOME/workspace"
cat >"$HOME/workspace/status.json" <<JSON
{"complete": true, "needs_input": false, "task_mode": "code", "pr_url": "https://github.com/o/r/pull/9"}
JSON
exit 0
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"
    export BACKFLOW_SKILLS_DIR="$DIR/skills"

    export PROMPT="implement a thing"
    export TASK_ID="bf_auto"
    export TASK_MODE="auto"
    export ANTHROPIC_API_KEY="sk-test"

    if ! "$ENTRYPOINT" >/dev/null 2>&1; then
        fail "auto-mode entrypoint should succeed when the auto bundle exists"
    fi
    for sub in auto code review; do
        if [ ! -f "$tmp/.claude/skills/${sub}/SKILL.md" ]; then
            fail "auto mode: expected ~/.claude/skills/${sub}/SKILL.md to be installed for dispatch"
        fi
    done
)
pass "auto mode installs auto + code + review bundles for runtime dispatch"

# --- Idempotent skill install: pre-existing destination must not nest ---
# Pins that re-running the entrypoint against a HOME where the skill bundle
# is already installed doesn't produce ~/.claude/skills/code/code/SKILL.md.
# `cp -r src dst` nests src inside dst when dst already exists; the entrypoint
# must guard against that.
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude/skills/code"
    # Pre-seed the destination as if a previous run had already installed it.
    echo "stale" > "$tmp/.claude/skills/code/SKILL.md"

    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
mkdir -p "$HOME/workspace"
cat >"$HOME/workspace/status.json" <<JSON
{"complete": true, "needs_input": false, "task_mode": "code"}
JSON
exit 0
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"
    export BACKFLOW_SKILLS_DIR="$DIR/skills"

    export PROMPT="do something"
    export TASK_ID="bf_test"
    export TASK_MODE="code"
    export ANTHROPIC_API_KEY="sk-test"

    "$ENTRYPOINT" >/dev/null 2>&1 || true

    if [ -e "$tmp/.claude/skills/code/code" ]; then
        fail "idempotent install: nested ~/.claude/skills/code/code/ exists; cp -r nested src under existing dst"
    fi
    if [ ! -f "$tmp/.claude/skills/code/SKILL.md" ]; then
        fail "idempotent install: expected ~/.claude/skills/code/SKILL.md to exist after re-run"
    fi
)
pass "skill install is idempotent when destination already exists"

# --- Review skill bundle installs end-to-end ---
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude"

    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
mkdir -p "$HOME/workspace"
cat >"$HOME/workspace/status.json" <<JSON
{"complete": true, "needs_input": false, "task_mode": "review", "pr_url": "https://github.com/o/r/pull/1"}
JSON
exit 0
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"
    export BACKFLOW_SKILLS_DIR="$DIR/skills"

    export PROMPT="review https://github.com/o/r/pull/1"
    export TASK_ID="bf_test"
    export TASK_MODE="review"
    export ANTHROPIC_API_KEY="sk-test"

    if ! "$ENTRYPOINT" >/dev/null 2>&1; then
        fail "review skill happy path: expected exit 0"
    fi
    if [ ! -f "$tmp/.claude/skills/review/SKILL.md" ]; then
        fail "review skill install: expected ~/.claude/skills/review/SKILL.md"
    fi
    # Pin a real-skill anchor that the slice 5 stub did not contain, so a
    # regression to the placeholder bundle would fail this test.
    if ! grep -q "Post the review" "$tmp/.claude/skills/review/SKILL.md"; then
        fail "review skill: expected real review instructions, got the slice 5 stub"
    fi
)
pass "installs review skill and runs to completion"

# --- Read skill bundle ships its helper scripts ---
(
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    export HOME="$tmp"
    mkdir -p "$tmp/.claude"

    cat >"$tmp/claude" <<'EOF'
#!/usr/bin/env bash
mkdir -p "$HOME/workspace"
cat >"$HOME/workspace/status.json" <<JSON
{"complete": true, "needs_input": false, "task_mode": "read", "url": "https://example.com", "title": "x", "tldr": "y", "tags": [], "keywords": [], "people": [], "orgs": [], "novelty_verdict": "novel", "connections": [], "summary_markdown": "z"}
JSON
exit 0
EOF
    chmod +x "$tmp/claude"
    export PATH="$tmp:$PATH"
    export BACKFLOW_SKILLS_DIR="$DIR/skills"

    export PROMPT="https://example.com"
    export TASK_ID="bf_test"
    export TASK_MODE="read"
    export ANTHROPIC_API_KEY="sk-test"

    if ! "$ENTRYPOINT" >/dev/null 2>&1; then
        fail "read skill happy path: expected exit 0"
    fi
    for helper in read-embed.sh read-similar.sh read-lookup.sh; do
        if [ ! -x "$tmp/.claude/skills/read/${helper}" ]; then
            fail "read skill: helper ${helper} should be installed and executable at ~/.claude/skills/read/"
        fi
    done
)
pass "installs read skill with helper scripts (read-embed.sh, read-similar.sh, read-lookup.sh)"

# --- send-email.sh: happy-path POST shape against fake Resend server ---
(
    tmp=$(mktemp -d)
    capture="$tmp/captured-request"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture" "$port_file" "$pid_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(capture, "w") as f:
            json.dump({
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }, f)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"id":"fake"}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    # Wait for server to bind a port (cap at ~3s)
    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email happy path: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    # Stub status.json with a populated reading.
    cat >"$tmp/status.json" <<'JSON'
{
  "url": "https://example.com/post",
  "title": "Example Post Title",
  "tldr": "A pithy one-line summary of the post."
}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_emailtest"

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email happy path: script exited non-zero"
    fi

    if [ ! -s "$capture" ]; then
        fail "send-email happy path: fake server did not record any request"
    fi
    method=$(jq -r '.method' "$capture")
    if [ "$method" != "POST" ]; then
        fail "send-email happy path: expected POST, got $method"
    fi
    path=$(jq -r '.path' "$capture")
    if [ "$path" != "/emails" ]; then
        fail "send-email happy path: expected path /emails, got $path"
    fi
    auth=$(jq -r '.headers.Authorization' "$capture")
    if [ "$auth" != "Bearer re_test" ]; then
        fail "send-email happy path: expected Authorization 'Bearer re_test', got $auth"
    fi
    ctype=$(jq -r '.headers["Content-Type"]' "$capture")
    if [ "$ctype" != "application/json" ]; then
        fail "send-email happy path: expected Content-Type application/json, got $ctype"
    fi
    body_from=$(jq -r '.body | fromjson | .from' "$capture")
    if [ "$body_from" != "from@example.com" ]; then
        fail "send-email happy path: expected body.from from@example.com, got $body_from"
    fi
    body_to=$(jq -r '.body | fromjson | .to[0]' "$capture")
    if [ "$body_to" != "to@example.com" ]; then
        fail "send-email happy path: expected body.to[0] to@example.com, got $body_to"
    fi
    body_subject=$(jq -r '.body | fromjson | .subject' "$capture")
    if [ "$body_subject" != "Example Post Title" ]; then
        fail "send-email happy path: expected body.subject 'Example Post Title', got $body_subject"
    fi
    body_text=$(jq -r '.body | fromjson | .text' "$capture")
    for needle in "https://example.com/post" "Example Post Title" "A pithy one-line summary" "Task: bf_emailtest"; do
        if [[ "$body_text" != *"$needle"* ]]; then
            fail "send-email happy path: body.text missing '$needle', got: $body_text"
        fi
    done
)
pass "send-email.sh POSTs to /emails with Bearer auth and JSON body containing from/to/subject/text"

# --- send-email.sh: no-op when RESEND_API_KEY is unset ---
(
    tmp=$(mktemp -d)
    capture="$tmp/captured-request"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture" "$port_file" "$pid_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        with open(capture, "w") as f:
            f.write("posted")
        self.send_response(200); self.end_headers()
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email no-op: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{"url":"https://example.com","title":"x","tldr":"y"}
JSON

    unset RESEND_API_KEY
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_noop"

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email no-op: script should exit 0 when RESEND_API_KEY is unset"
    fi
    if [ -s "$capture" ]; then
        fail "send-email no-op: script must not POST when RESEND_API_KEY is unset (capture: $(cat "$capture"))"
    fi
)
pass "send-email.sh exits 0 without POSTing when RESEND_API_KEY is unset"

# --- send-email.sh: rich payload — all optional fields populated ---
(
    tmp=$(mktemp -d)
    capture="$tmp/captured-request"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture" "$port_file" "$pid_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(capture, "w") as f:
            json.dump({
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }, f)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"id":"fake"}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email rich: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{
  "url": "https://example.com/post",
  "title": "Concurrency in Go",
  "tldr": "A practical tour of Go concurrency primitives.",
  "tags": ["go", "systems"],
  "keywords": ["channels", "goroutines"],
  "people": ["Alice"],
  "orgs": ["ACME"],
  "novelty_verdict": "novel",
  "connections": [
    {"reading_id": "bf_x", "reason": "covers same topic"},
    {"reading_id": "bf_y", "reason": "different angle"}
  ],
  "summary_markdown": "## Summary\n\nKey points about concurrency."
}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_rich"

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email rich: script exited non-zero"
    fi
    if [ ! -s "$capture" ]; then
        fail "send-email rich: fake server did not record any request"
    fi
    body_subject=$(jq -r '.body | fromjson | .subject' "$capture")
    if [ "$body_subject" != "Concurrency in Go" ]; then
        fail "send-email rich: expected subject 'Concurrency in Go', got '$body_subject'"
    fi
    body_text=$(jq -r '.body | fromjson | .text' "$capture")
    for needle in \
        "URL: https://example.com/post" \
        "Title: Concurrency in Go" \
        "Novelty: novel" \
        "Tags: go, systems" \
        "Keywords: channels, goroutines" \
        "People: Alice" \
        "Orgs: ACME" \
        "TL;DR: A practical tour of Go concurrency primitives." \
        "Summary:" \
        "## Summary" \
        "Key points about concurrency." \
        "Connections:" \
        "- bf_x: covers same topic" \
        "- bf_y: different angle" \
        "Task: bf_rich"; do
        if [[ "$body_text" != *"$needle"* ]]; then
            fail "send-email rich: body.text missing '$needle', got: $body_text"
        fi
    done
)
pass "send-email.sh renders all optional sections when fields are populated"

# --- send-email.sh: sparse payload + hostname fallback ---
(
    tmp=$(mktemp -d)
    capture="$tmp/captured-request"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture" "$port_file" "$pid_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(capture, "w") as f:
            json.dump({"body": body}, f)
        self.send_response(200); self.end_headers()
        self.wfile.write(b'{"id":"fake"}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email sparse: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{
  "url": "https://news.example.org/some/path",
  "title": "",
  "tldr": "Quick summary."
}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_sparse"

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email sparse: script exited non-zero"
    fi
    if [ ! -s "$capture" ]; then
        fail "send-email sparse: fake server did not record any request"
    fi
    body_subject=$(jq -r '.body | fromjson | .subject' "$capture")
    if [ "$body_subject" != "news.example.org" ]; then
        fail "send-email sparse: expected subject 'news.example.org' (hostname fallback), got '$body_subject'"
    fi
    body_text=$(jq -r '.body | fromjson | .text' "$capture")
    for needle in \
        "URL: https://news.example.org/some/path" \
        "TL;DR: Quick summary." \
        "Task: bf_sparse"; do
        if [[ "$body_text" != *"$needle"* ]]; then
            fail "send-email sparse: body.text missing '$needle', got: $body_text"
        fi
    done
    for forbidden in "Tags:" "Keywords:" "People:" "Orgs:" "Novelty:" "Summary:" "Connections:"; do
        if [[ "$body_text" == *"$forbidden"* ]]; then
            fail "send-email sparse: body.text should not contain '$forbidden', got: $body_text"
        fi
    done
)
pass "send-email.sh falls back to URL hostname for subject and skips empty optional sections"

# --- send-email.sh: mixed populated/empty optional fields ---
(
    tmp=$(mktemp -d)
    capture="$tmp/captured-request"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture" "$port_file" "$pid_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(capture, "w") as f:
            json.dump({"body": body}, f)
        self.send_response(200); self.end_headers()
        self.wfile.write(b'{"id":"fake"}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email mixed: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{
  "url": "https://example.com/p",
  "title": "Mixed Post",
  "tldr": "A summary.",
  "tags": ["one"],
  "keywords": [],
  "people": [],
  "orgs": [],
  "novelty_verdict": "",
  "connections": [
    {"reading_id": "bf_y", "reason": "r"}
  ],
  "summary_markdown": ""
}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_mixed"

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email mixed: script exited non-zero"
    fi
    if [ ! -s "$capture" ]; then
        fail "send-email mixed: fake server did not record any request"
    fi
    body_text=$(jq -r '.body | fromjson | .text' "$capture")
    for needle in \
        "Title: Mixed Post" \
        "Tags: one" \
        "Connections:" \
        "- bf_y: r" \
        "Task: bf_mixed"; do
        if [[ "$body_text" != *"$needle"* ]]; then
            fail "send-email mixed: body.text missing '$needle', got: $body_text"
        fi
    done
    for forbidden in "Keywords:" "People:" "Orgs:" "Novelty:" "Summary:"; do
        if [[ "$body_text" == *"$forbidden"* ]]; then
            fail "send-email mixed: body.text should not contain '$forbidden', got: $body_text"
        fi
    done
)
pass "send-email.sh renders only populated sections in a mixed payload"

# --- send-email.sh: Idempotency-Key header is set on the POST ---
(
    tmp=$(mktemp -d)
    capture_dir="$tmp/captures"
    mkdir -p "$capture_dir"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    responses_file="$tmp/responses.txt"
    printf '200\n' >"$responses_file"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture_dir" "$port_file" "$pid_file" "$responses_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture_dir = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]
responses_file = sys.argv[4]

with open(responses_file) as f:
    responses = [int(line.strip()) for line in f if line.strip()]

state = {"count": 0}

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        i = state["count"]
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(os.path.join(capture_dir, f"request-{i}.json"), "w") as f:
            json.dump({
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }, f)
        code = responses[i] if i < len(responses) else responses[-1]
        state["count"] += 1
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email idempotency-key: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{"url":"https://example.com/post","title":"Example","tldr":"summary"}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_idemkey"
    export RESEND_RETRY_BASE_DELAY_SEC=0

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email idempotency-key: script exited non-zero"
    fi

    if [ ! -s "$capture_dir/request-0.json" ]; then
        fail "send-email idempotency-key: no request captured"
    fi
    idem=$(jq -r '.headers["Idempotency-Key"]' "$capture_dir/request-0.json")
    if [ "$idem" != "bf_idemkey:read.completed" ]; then
        fail "send-email idempotency-key: expected 'bf_idemkey:read.completed', got '$idem'"
    fi
)
pass "send-email.sh sends Idempotency-Key: <task_id>:read.completed header"

# --- send-email.sh: retries on 5xx, succeeds on attempt 2 ---
(
    tmp=$(mktemp -d)
    capture_dir="$tmp/captures"
    mkdir -p "$capture_dir"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    responses_file="$tmp/responses.txt"
    printf '500\n200\n' >"$responses_file"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture_dir" "$port_file" "$pid_file" "$responses_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture_dir = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]
responses_file = sys.argv[4]

with open(responses_file) as f:
    responses = [int(line.strip()) for line in f if line.strip()]

state = {"count": 0}

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        i = state["count"]
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(os.path.join(capture_dir, f"request-{i}.json"), "w") as f:
            json.dump({
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }, f)
        code = responses[i] if i < len(responses) else responses[-1]
        state["count"] += 1
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email retry-5xx: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{"url":"https://example.com/post","title":"Example","tldr":"summary"}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_retry5xx"
    export RESEND_RETRY_BASE_DELAY_SEC=0

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email retry-5xx: script exited non-zero"
    fi

    if [ ! -s "$capture_dir/request-0.json" ]; then
        fail "send-email retry-5xx: first request not captured"
    fi
    if [ ! -s "$capture_dir/request-1.json" ]; then
        fail "send-email retry-5xx: second request not captured (no retry attempted?)"
    fi
    if [ -s "$capture_dir/request-2.json" ]; then
        fail "send-email retry-5xx: should have stopped at 2 requests, third was made"
    fi
    idem0=$(jq -r '.headers["Idempotency-Key"]' "$capture_dir/request-0.json")
    idem1=$(jq -r '.headers["Idempotency-Key"]' "$capture_dir/request-1.json")
    if [ "$idem0" != "$idem1" ]; then
        fail "send-email retry-5xx: idempotency keys differ across attempts ('$idem0' vs '$idem1')"
    fi
    if [ "$idem0" != "bf_retry5xx:read.completed" ]; then
        fail "send-email retry-5xx: unexpected idempotency key '$idem0'"
    fi
)
pass "send-email.sh retries on 5xx and succeeds on attempt 2 with the same Idempotency-Key"

# --- send-email.sh: gives up after 3 attempts of 5xx, exits 0 ---
(
    tmp=$(mktemp -d)
    capture_dir="$tmp/captures"
    mkdir -p "$capture_dir"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    responses_file="$tmp/responses.txt"
    printf '500\n500\n500\n' >"$responses_file"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture_dir" "$port_file" "$pid_file" "$responses_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture_dir = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]
responses_file = sys.argv[4]

with open(responses_file) as f:
    responses = [int(line.strip()) for line in f if line.strip()]

state = {"count": 0}

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        i = state["count"]
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(os.path.join(capture_dir, f"request-{i}.json"), "w") as f:
            json.dump({
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }, f)
        code = responses[i] if i < len(responses) else responses[-1]
        state["count"] += 1
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email give-up: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{"url":"https://example.com/post","title":"Example","tldr":"summary"}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_giveup"
    export RESEND_RETRY_BASE_DELAY_SEC=0

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email give-up: script must exit 0 even after exhausting retries"
    fi

    for i in 0 1 2; do
        if [ ! -s "$capture_dir/request-${i}.json" ]; then
            fail "send-email give-up: expected 3 captured requests, missing request-${i}.json"
        fi
    done
    if [ -s "$capture_dir/request-3.json" ]; then
        fail "send-email give-up: a 4th request was made (should stop at 3)"
    fi
)
pass "send-email.sh gives up after 3 attempts of 5xx and exits 0"

# --- send-email.sh: 4xx response — no retry, exits 0 ---
(
    tmp=$(mktemp -d)
    capture_dir="$tmp/captures"
    mkdir -p "$capture_dir"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    responses_file="$tmp/responses.txt"
    printf '400\n200\n200\n' >"$responses_file"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture_dir" "$port_file" "$pid_file" "$responses_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture_dir = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]
responses_file = sys.argv[4]

with open(responses_file) as f:
    responses = [int(line.strip()) for line in f if line.strip()]

state = {"count": 0}

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        i = state["count"]
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(os.path.join(capture_dir, f"request-{i}.json"), "w") as f:
            json.dump({
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }, f)
        code = responses[i] if i < len(responses) else responses[-1]
        state["count"] += 1
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email no-retry-4xx: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{"url":"https://example.com/post","title":"Example","tldr":"summary"}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_no4xxretry"
    export RESEND_RETRY_BASE_DELAY_SEC=0

    SCRIPT="$DIR/skills/read/send-email.sh"
    if ! "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1; then
        fail "send-email no-retry-4xx: script must exit 0 on 4xx"
    fi

    if [ ! -s "$capture_dir/request-0.json" ]; then
        fail "send-email no-retry-4xx: first request not captured"
    fi
    if [ -s "$capture_dir/request-1.json" ]; then
        fail "send-email no-retry-4xx: a second request was made (4xx should not retry)"
    fi
)
pass "send-email.sh does not retry on 4xx and exits 0"

# --- send-email.sh: Idempotency-Key is identical across all 3 retry attempts ---
(
    tmp=$(mktemp -d)
    capture_dir="$tmp/captures"
    mkdir -p "$capture_dir"
    pid_file="$tmp/server.pid"
    port_file="$tmp/port"
    responses_file="$tmp/responses.txt"
    printf '500\n500\n500\n' >"$responses_file"
    trap '[ -f "$pid_file" ] && kill "$(cat "$pid_file")" 2>/dev/null || true; rm -rf "$tmp"' EXIT

    python3 - "$capture_dir" "$port_file" "$pid_file" "$responses_file" >/dev/null 2>&1 <<'PYEOF' &
import json, sys, os
from http.server import BaseHTTPRequestHandler, HTTPServer

capture_dir = sys.argv[1]
port_file = sys.argv[2]
pid_file = sys.argv[3]
responses_file = sys.argv[4]

with open(responses_file) as f:
    responses = [int(line.strip()) for line in f if line.strip()]

state = {"count": 0}

class H(BaseHTTPRequestHandler):
    def do_POST(self):
        i = state["count"]
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8")
        with open(os.path.join(capture_dir, f"request-{i}.json"), "w") as f:
            json.dump({
                "method": self.command,
                "path": self.path,
                "headers": dict(self.headers),
                "body": body,
            }, f)
        code = responses[i] if i < len(responses) else responses[-1]
        state["count"] += 1
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{}')
    def log_message(self, *a, **kw):
        pass

srv = HTTPServer(("127.0.0.1", 0), H)
with open(port_file, "w") as f:
    f.write(str(srv.server_address[1]))
with open(pid_file, "w") as f:
    f.write(str(os.getpid()))
srv.serve_forever()
PYEOF

    for _ in $(seq 1 30); do
        if [ -s "$port_file" ]; then break; fi
        sleep 0.1
    done
    if [ ! -s "$port_file" ]; then
        fail "send-email key-stability: fake Resend server did not start"
    fi
    PORT=$(cat "$port_file")

    cat >"$tmp/status.json" <<'JSON'
{"url":"https://example.com/post","title":"Example","tldr":"summary"}
JSON

    export RESEND_API_KEY="re_test"
    export NOTIFY_EMAIL_FROM="from@example.com"
    export NOTIFY_EMAIL_TO="to@example.com"
    export RESEND_BASE_URL="http://127.0.0.1:$PORT"
    export TASK_ID="bf_keystable"
    export RESEND_RETRY_BASE_DELAY_SEC=0

    SCRIPT="$DIR/skills/read/send-email.sh"
    "$SCRIPT" "$tmp/status.json" >/dev/null 2>&1 || true

    expected="bf_keystable:read.completed"
    for i in 0 1 2; do
        if [ ! -s "$capture_dir/request-${i}.json" ]; then
            fail "send-email key-stability: missing request-${i}.json"
        fi
        idem=$(jq -r '.headers["Idempotency-Key"]' "$capture_dir/request-${i}.json")
        if [ "$idem" != "$expected" ]; then
            fail "send-email key-stability: attempt ${i} carried '$idem', expected '$expected'"
        fi
    done
)
pass "send-email.sh sends a stable Idempotency-Key across all retry attempts"

echo
echo "All skill-agent entrypoint tests passed."
