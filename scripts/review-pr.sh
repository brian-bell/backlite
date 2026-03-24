#!/usr/bin/env bash
set -euo pipefail

# Review an existing PR via the Backflow API
#
# Usage:
#   ./scripts/review-pr.sh <pr_url> [options]
#
# Examples:
#   ./scripts/review-pr.sh https://github.com/org/repo/pull/42
#   ./scripts/review-pr.sh https://github.com/org/repo/pull/42 --model claude-sonnet-4-6
#   ./scripts/review-pr.sh https://github.com/org/repo/pull/42 --harness codex
#   ./scripts/review-pr.sh https://github.com/org/repo/pull/42 --prompt "Focus on security issues"
#   ./scripts/review-pr.sh https://github.com/org/repo/pull/42 --budget 5 --effort low

BACKFLOW_URL="${BACKFLOW_URL:-http://localhost:8080}"

usage() {
    cat <<USAGE
Usage: $(basename "$0") <pr_url> [options]

Arguments:
  pr_url                  Full PR URL (e.g. https://github.com/org/repo/pull/42)

Options:
  --prompt <text>         Custom review instructions
  --harness <name>        Agent harness: claude_code or codex (defaults to server setting)
  --model <model>         Model to use (defaults to server setting for the selected harness)
  --effort <level>        Reasoning effort: low, medium, high (defaults to server setting)
  --budget <usd>          Max budget in USD
  --runtime <sec>         Max runtime in seconds
  --turns <n>             Max conversation turns
  --claude-md <text>      Extra CLAUDE.md content to inject
  --context <text>        Additional context for the review
  --env <KEY=VALUE>       Environment variable (can repeat)
USAGE
    exit 1
}

if [ $# -lt 1 ]; then
    usage
fi

PR_URL="$1"
shift

# Validate that it looks like a PR URL
if ! [[ "$PR_URL" =~ /pull/[0-9]+(/|$) ]]; then
    echo "Error: Expected a PR URL like https://github.com/owner/repo/pull/123, got '$PR_URL'" >&2
    exit 1
fi

# Defaults — empty means "let the server decide"
PROMPT=""
HARNESS=""
MODEL=""
EFFORT=""
BUDGET=""
RUNTIME=""
TURNS=""
CLAUDE_MD=""
CONTEXT=""
declare -a ENV_VARS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --prompt)       PROMPT="$2"; shift 2 ;;
        --harness)      HARNESS="$2"; shift 2 ;;
        --model)        MODEL="$2"; shift 2 ;;
        --effort)       EFFORT="$2"; shift 2 ;;
        --budget)       BUDGET="$2"; shift 2 ;;
        --runtime)      RUNTIME="$2"; shift 2 ;;
        --turns)        TURNS="$2"; shift 2 ;;
        --claude-md)    CLAUDE_MD="$2"; shift 2 ;;
        --context)      CONTEXT="$2"; shift 2 ;;
        --env)          ENV_VARS+=("$2"); shift 2 ;;
        *)              echo "Unknown option: $1"; usage ;;
    esac
done

# Build JSON payload
JSON=$(jq -n \
    --arg pr_url "$PR_URL" \
    --arg prompt "$PROMPT" \
    --arg harness "$HARNESS" \
    --arg model "$MODEL" \
    --arg effort "$EFFORT" \
    --arg budget "$BUDGET" \
    --arg runtime "$RUNTIME" \
    --arg turns "$TURNS" \
    --arg claude_md "$CLAUDE_MD" \
    --arg context "$CONTEXT" \
    '{
        prompt: ("Review " + $pr_url)
    }
    + if $prompt != "" then {prompt: $prompt} else {} end
    + if $harness != "" then {harness: $harness} else {} end
    + if $model != "" then {model: $model} else {} end
    + if $effort != "" then {effort: $effort} else {} end
    + if $budget != "" then {max_budget_usd: ($budget | tonumber)} else {} end
    + if $runtime != "" then {max_runtime_sec: ($runtime | tonumber)} else {} end
    + if $turns != "" then {max_turns: ($turns | tonumber)} else {} end
    + if $claude_md != "" then {claude_md: $claude_md} else {} end
    + if $context != "" then {context: $context} else {} end
    ')

# Add env vars if any
if [ ${#ENV_VARS[@]} -gt 0 ]; then
    ENV_JSON="{}"
    for pair in "${ENV_VARS[@]}"; do
        key="${pair%%=*}"
        value="${pair#*=}"
        ENV_JSON=$(echo "$ENV_JSON" | jq --arg k "$key" --arg v "$value" '. + {($k): $v}')
    done
    JSON=$(echo "$JSON" | jq --argjson env "$ENV_JSON" '. + {env_vars: $env}')
fi

# Send request
RESPONSE=$(curl -s -w "\n%{http_code}" \
    -X POST "${BACKFLOW_URL}/api/v1/tasks" \
    -H "Content-Type: application/json" \
    -d "$JSON")

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" = "201" ]; then
    echo "$BODY" | jq .
    TASK_ID=$(echo "$BODY" | jq -r '.data.id')
    echo ""
    echo "Useful commands:"
    echo "  # Get task status"
    echo "  curl -s ${BACKFLOW_URL}/api/v1/tasks/${TASK_ID} | jq ."
    echo ""
    echo "  # Stream logs"
    echo "  curl -s ${BACKFLOW_URL}/api/v1/tasks/${TASK_ID}/logs"
    echo ""
    echo "  # Cancel task"
    echo "  curl -s -X DELETE ${BACKFLOW_URL}/api/v1/tasks/${TASK_ID} | jq ."
else
    echo "Error (HTTP $HTTP_CODE):" >&2
    echo "$BODY" | jq . 2>/dev/null || echo "$BODY" >&2
    exit 1
fi
