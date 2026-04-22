#!/usr/bin/env bash
# Tests for the env-var-driven repo metadata resolution in entrypoint.sh.
# Mirrors the logic of the REPO_URL-set branch without invoking the LLM.
set -euo pipefail

PASS=0
FAIL=0

# Mirrors the extraction used in entrypoint.sh when TASK_TYPE=review and
# REPO_URL came from env — we still need to pluck the PR URL from the prompt.
extract_pr_url() {
    local prompt="$1"
    printf '%s\n' "$prompt" | grep -oE 'https?://github\.com/[^/[:space:]]+/[^/[:space:]]+/pull/[0-9]+' | head -1 || true
}

# Mirrors the TASK_MODE → TASK_TYPE mapping in entrypoint.sh.
map_task_type() {
    case "${1:-code}" in
        review) echo "review" ;;
        *)      echo "code" ;;
    esac
}

assert_eq() {
    local name="$1" want="$2" got="$3"
    if [ "$want" = "$got" ]; then
        echo "PASS: $name"
        PASS=$((PASS + 1))
    else
        echo "FAIL: $name — want=$want got=$got"
        FAIL=$((FAIL + 1))
    fi
}

# --- PR URL extraction ---

assert_eq "PR URL bare in prompt" \
    "https://github.com/owner/repo/pull/42" \
    "$(extract_pr_url 'please review https://github.com/owner/repo/pull/42 thoroughly')"

assert_eq "PR URL with surrounding punctuation" \
    "https://github.com/owner/repo/pull/42" \
    "$(extract_pr_url 'see (https://github.com/owner/repo/pull/42).')"

assert_eq "no PR URL, repo URL only" \
    "" \
    "$(extract_pr_url 'work on https://github.com/owner/repo and add tests')"

assert_eq "issue URL is not a PR URL" \
    "" \
    "$(extract_pr_url 'fix https://github.com/owner/repo/issues/17')"

assert_eq "first of multiple PR URLs" \
    "https://github.com/owner/repo/pull/1" \
    "$(extract_pr_url 'compare https://github.com/owner/repo/pull/1 with https://github.com/owner/repo/pull/2')"

assert_eq "empty prompt yields empty" \
    "" \
    "$(extract_pr_url '')"

# --- TASK_MODE → TASK_TYPE mapping ---

assert_eq "review mode maps to review" "review" "$(map_task_type review)"
assert_eq "code mode maps to code"     "code"   "$(map_task_type code)"
assert_eq "read mode falls to code"    "code"   "$(map_task_type read)"
assert_eq "unset mode defaults to code" "code"  "$(map_task_type '')"
assert_eq "unknown mode defaults to code" "code" "$(map_task_type garbage)"

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] || exit 1
