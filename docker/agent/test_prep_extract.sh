#!/usr/bin/env bash
# Tests for codex prep stage output extraction.
# Exercises the same sed pipeline used in entrypoint.sh for the codex harness.
set -euo pipefail

PASS=0
FAIL=0

# Mirrors the extraction logic from entrypoint.sh (codex branch):
# Strip fences, then extract the first balanced JSON object via brace depth.
extract_codex_prep() {
    local file="$1"
    sed '/^```/d' "$file" | awk '
        /^\s*\{/ && !started { started=1 }
        started {
            print
            for (i=1; i<=length($0); i++) {
                c = substr($0, i, 1)
                if (c == "{") depth++
                else if (c == "}") depth--
            }
            if (depth == 0) exit
        }'
}

assert_json() {
    local test_name="$1"
    local input="$2"
    local expected_repo="$3"

    local tmpfile
    tmpfile=$(mktemp)
    echo "$input" > "$tmpfile"

    local result
    result=$(extract_codex_prep "$tmpfile")
    rm -f "$tmpfile"

    local repo
    repo=$(echo "$result" | jq -r '.repo_url // empty' 2>/dev/null) || {
        echo "FAIL: $test_name — not valid JSON"
        echo "  Got: ${result:0:200}"
        FAIL=$((FAIL + 1))
        return
    }

    if [ "$repo" = "$expected_repo" ]; then
        echo "PASS: $test_name"
        PASS=$((PASS + 1))
    else
        echo "FAIL: $test_name — expected repo=$expected_repo, got repo=$repo"
        FAIL=$((FAIL + 1))
    fi
}

# --- Test cases ---

# 1. Clean JSON (no banner)
assert_json "clean JSON" \
    '{"repo_url":"https://github.com/owner/repo","target_branch":"main","task_type":"code"}' \
    "https://github.com/owner/repo"

# 2. Codex banner + JSON — the real bug
BANNER_INPUT="OpenAI Codex v0.116.0 (research preview)
--------
workdir: /home/agent
model: gpt-5.4
provider: openai
approval: never
sandbox: danger-full-access
reasoning effort: none
reasoning summaries: none
session id: 019d4fed-d66a-76b2-833e-d00b81c0709d
--------
user
Analyze this task prompt...

assistant
{\"repo_url\":\"https://github.com/owner/repo\",\"target_branch\":\"main\",\"task_type\":\"code\"}"

assert_json "codex banner + JSON" "$BANNER_INPUT" "https://github.com/owner/repo"

# 3. Codex banner + JSON with markdown fences
FENCED_INPUT='OpenAI Codex v0.116.0 (research preview)
--------
workdir: /home/agent
model: gpt-5.4
--------
user
Analyze this...

assistant
```json
{"repo_url":"https://github.com/owner/repo","target_branch":"develop","task_type":"review","pr_url":"https://github.com/owner/repo/pull/42"}
```'

assert_json "codex banner + fenced JSON" "$FENCED_INPUT" "https://github.com/owner/repo"

# 4. Multi-line JSON after banner
MULTILINE_INPUT='OpenAI Codex v0.116.0 (research preview)
--------
workdir: /home/agent
--------
assistant
{
  "repo_url": "https://github.com/foo/bar",
  "target_branch": "main",
  "task_type": "code"
}'

assert_json "codex banner + multi-line JSON" "$MULTILINE_INPUT" "https://github.com/foo/bar"

# 5. Codex banner + JSON + trailing token stats (real-world codex output)
TOKENS_INPUT='OpenAI Codex v0.116.0 (research preview)
--------
workdir: /home/agent
model: gpt-5.4
provider: openai
session id: 019d4fed-d66a-76b2-833e-d00b81c0709d
--------
user
Analyze this task prompt...

assistant
{"repo_url":"https://github.com/brian-bell/simple-pilot-logbook","target_branch":"main","task_type":"review","pr_url":"https://github.com/brian-bell/simple-pilot-logbook/pull/8"}
tokens used
12,295
{"'

assert_json "codex banner + JSON + trailing token stats" "$TOKENS_INPUT" "https://github.com/brian-bell/simple-pilot-logbook"

# 6. Multi-line JSON + trailing token stats
MULTILINE_TOKENS='OpenAI Codex v0.116.0 (research preview)
--------
assistant
{
  "repo_url": "https://github.com/foo/bar",
  "target_branch": "main",
  "task_type": "code"
}
tokens used
5,432'

assert_json "multi-line JSON + trailing token stats" "$MULTILINE_TOKENS" "https://github.com/foo/bar"

# --- Summary ---
echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
