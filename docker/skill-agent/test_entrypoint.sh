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

echo
echo "All skill-agent entrypoint tests passed."
