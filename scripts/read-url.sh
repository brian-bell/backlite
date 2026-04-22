#!/usr/bin/env bash
set -euo pipefail

# Submit a URL for reading-mode summarization via the Backflow API.
#
# The reader container fetches the URL, drafts a TL;DR, and persists a row
# in the `readings` table (embedded via OpenAI). A duplicate URL is rejected
# at dispatch unless --force is passed.
#
# Usage:
#   ./scripts/read-url.sh <url> [options]
#
# Examples:
#   ./scripts/read-url.sh https://example.com/article
#   ./scripts/read-url.sh https://example.com/article --force
#   ./scripts/read-url.sh https://example.com/article --context "Focus on the caching section"
#   ./scripts/read-url.sh https://example.com/article --budget 0.5 --runtime 300

BACKFLOW_URL="${BACKFLOW_URL:-http://localhost:8080}"

usage() {
    cat <<USAGE
Usage: $(basename "$0") <url> [options]

Arguments:
  url                     URL to fetch and summarize

Options:
  --force                 Overwrite an existing reading row for this URL
  --harness <name>        Agent harness: claude_code or codex (defaults to server setting)
  --model <model>         Model to use (defaults to server setting for the selected harness)
  --effort <level>        Reasoning effort: low, medium, high (defaults to server setting)
  --budget <usd>          Max budget in USD
  --runtime <sec>         Max runtime in seconds
  --turns <n>             Max conversation turns
  --context <text>        Additional context for the reader
  --claude-md <text>      Extra CLAUDE.md content to inject
  --no-save-output        Skip saving agent output to disk
  --env <KEY=VALUE>       Environment variable (can repeat)
USAGE
    exit 1
}

if [ $# -lt 1 ]; then
    usage
fi

URL="$1"
shift

if ! [[ "$URL" =~ ^https?:// ]]; then
    echo "Error: expected an http(s) URL, got '$URL'" >&2
    exit 1
fi

# Defaults — empty means "let the server decide"
FORCE=""
HARNESS=""
MODEL=""
EFFORT=""
BUDGET=""
RUNTIME=""
TURNS=""
CONTEXT=""
CLAUDE_MD=""
SAVE_AGENT_OUTPUT=""
declare -a ENV_VARS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --force)          FORCE=true; shift ;;
        --harness)        HARNESS="$2"; shift 2 ;;
        --model)          MODEL="$2"; shift 2 ;;
        --effort)         EFFORT="$2"; shift 2 ;;
        --budget)         BUDGET="$2"; shift 2 ;;
        --runtime)        RUNTIME="$2"; shift 2 ;;
        --turns)          TURNS="$2"; shift 2 ;;
        --context)        CONTEXT="$2"; shift 2 ;;
        --claude-md)      CLAUDE_MD="$2"; shift 2 ;;
        --no-save-output) SAVE_AGENT_OUTPUT=false; shift ;;
        --env)            ENV_VARS+=("$2"); shift 2 ;;
        *)                echo "Unknown option: $1"; usage ;;
    esac
done

JSON=$(jq -n \
    --arg url "$URL" \
    --arg force "$FORCE" \
    --arg harness "$HARNESS" \
    --arg model "$MODEL" \
    --arg effort "$EFFORT" \
    --arg budget "$BUDGET" \
    --arg runtime "$RUNTIME" \
    --arg turns "$TURNS" \
    --arg context "$CONTEXT" \
    --arg claude_md "$CLAUDE_MD" \
    --arg save_agent_output "$SAVE_AGENT_OUTPUT" \
    '{
        task_mode: "read",
        prompt: $url
    }
    + if $force != "" then {force: ($force == "true")} else {} end
    + if $harness != "" then {harness: $harness} else {} end
    + if $model != "" then {model: $model} else {} end
    + if $effort != "" then {effort: $effort} else {} end
    + if $budget != "" then {max_budget_usd: ($budget | tonumber)} else {} end
    + if $runtime != "" then {max_runtime_sec: ($runtime | tonumber)} else {} end
    + if $turns != "" then {max_turns: ($turns | tonumber)} else {} end
    + if $context != "" then {context: $context} else {} end
    + if $claude_md != "" then {claude_md: $claude_md} else {} end
    + if $save_agent_output != "" then {save_agent_output: ($save_agent_output == "true")} else {} end
    ')

if [ ${#ENV_VARS[@]} -gt 0 ]; then
    ENV_JSON="{}"
    for pair in "${ENV_VARS[@]}"; do
        key="${pair%%=*}"
        value="${pair#*=}"
        ENV_JSON=$(echo "$ENV_JSON" | jq --arg k "$key" --arg v "$value" '. + {($k): $v}')
    done
    JSON=$(echo "$JSON" | jq --argjson env "$ENV_JSON" '. + {env_vars: $env}')
fi

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
    echo "  # Fetch agent output"
    echo "  curl -s ${BACKFLOW_URL}/api/v1/tasks/${TASK_ID}/output"
    echo ""
    echo "  # Cancel task"
    echo "  curl -s -X DELETE ${BACKFLOW_URL}/api/v1/tasks/${TASK_ID} | jq ."
else
    echo "Error (HTTP $HTTP_CODE):" >&2
    echo "$BODY" | jq . 2>/dev/null || echo "$BODY" >&2
    exit 1
fi
