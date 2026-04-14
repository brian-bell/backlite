#!/usr/bin/env bash
set -euo pipefail

# Validation tests for the reader helper scripts. No network required:
# every case below is expected to fail before any API call is issued.

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

fail() {
    echo "FAIL: $1" >&2
    exit 1
}

expect_exit_nonzero() {
    local desc="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        fail "$desc: expected non-zero exit"
    fi
}

expect_stderr_contains() {
    local desc="$1"
    local needle="$2"
    shift 2
    local stderr
    stderr=$("$@" 2>&1 >/dev/null || true)
    if [[ "$stderr" != *"$needle"* ]]; then
        fail "$desc: expected stderr to contain '$needle' but got: $stderr"
    fi
}

# --- read-embed.sh ---
(
    unset OPENAI_API_KEY
    expect_stderr_contains "read-embed without OPENAI_API_KEY" "OPENAI_API_KEY" \
        "$DIR/read-embed.sh" "hello"
    expect_exit_nonzero "read-embed without OPENAI_API_KEY" \
        "$DIR/read-embed.sh" "hello"
)

(
    export OPENAI_API_KEY=dummy
    expect_stderr_contains "read-embed empty input" "empty" \
        bash -c "printf '' | '$DIR/read-embed.sh'"
    expect_exit_nonzero "read-embed empty input" \
        bash -c "printf '' | '$DIR/read-embed.sh'"
)

# --- read-lookup.sh ---
(
    unset SUPABASE_URL SUPABASE_ANON_KEY
    expect_stderr_contains "read-lookup missing env" "SUPABASE_URL" \
        "$DIR/read-lookup.sh" "https://example.com"
    expect_exit_nonzero "read-lookup missing env" \
        "$DIR/read-lookup.sh" "https://example.com"
)

(
    export SUPABASE_URL=https://example.supabase.co
    unset SUPABASE_ANON_KEY
    expect_stderr_contains "read-lookup missing anon key" "SUPABASE_ANON_KEY" \
        "$DIR/read-lookup.sh" "https://example.com"
    expect_exit_nonzero "read-lookup missing anon key" \
        "$DIR/read-lookup.sh" "https://example.com"
)

(
    export SUPABASE_URL=https://example.supabase.co
    export SUPABASE_ANON_KEY=dummy
    expect_stderr_contains "read-lookup missing url arg" "URL" \
        "$DIR/read-lookup.sh"
    expect_exit_nonzero "read-lookup missing url arg" \
        "$DIR/read-lookup.sh"
)

# --- read-similar.sh ---
(
    unset OPENAI_API_KEY SUPABASE_URL SUPABASE_ANON_KEY
    expect_stderr_contains "read-similar missing env" "OPENAI_API_KEY" \
        "$DIR/read-similar.sh" "hello"
    expect_exit_nonzero "read-similar missing env" \
        "$DIR/read-similar.sh" "hello"
)

(
    export OPENAI_API_KEY=dummy
    unset SUPABASE_URL SUPABASE_ANON_KEY
    expect_stderr_contains "read-similar missing supabase" "SUPABASE_URL" \
        "$DIR/read-similar.sh" "hello"
    expect_exit_nonzero "read-similar missing supabase" \
        "$DIR/read-similar.sh" "hello"
)

(
    export OPENAI_API_KEY=dummy
    export SUPABASE_URL=https://example.supabase.co
    unset SUPABASE_ANON_KEY
    expect_stderr_contains "read-similar missing anon key" "SUPABASE_ANON_KEY" \
        "$DIR/read-similar.sh" "hello"
    expect_exit_nonzero "read-similar missing anon key" \
        "$DIR/read-similar.sh" "hello"
)

(
    export OPENAI_API_KEY=dummy
    export SUPABASE_URL=https://example.supabase.co
    export SUPABASE_ANON_KEY=dummy
    expect_stderr_contains "read-similar empty input" "empty" \
        bash -c "printf '' | '$DIR/read-similar.sh'"
    expect_exit_nonzero "read-similar empty input" \
        bash -c "printf '' | '$DIR/read-similar.sh'"
)

echo "ok"
