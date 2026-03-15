#!/usr/bin/env bash
set -euo pipefail

# Create a task via the Backflow API
#
# Usage:
#   ./scripts/create-task.sh <repo_url> <prompt> [options]
#
# Examples:
#   ./scripts/create-task.sh https://github.com/org/repo "Fix the login bug"
#   ./scripts/create-task.sh https://github.com/org/repo "Add unit tests" --pr --model claude-sonnet-4-6
#   ./scripts/create-task.sh https://github.com/org/repo "Refactor auth" --pr --pr-title "Refactor auth module" --budget 20
#   ./scripts/create-task.sh https://github.com/org/repo "Fix bug" --effort low --model claude-sonnet-4-6

BACKFLOW_URL="${BACKFLOW_URL:-http://localhost:8080}"

usage() {
    cat <<USAGE
Usage: $(basename "$0") <repo_url> <prompt> [options]

Options:
  --branch <name>         Working branch name
  --target-branch <name>  Target branch (default: main)
  --model <model>         Claude model to use (default: claude-opus-4-6)
  --effort <level>        Reasoning effort: low, medium, high (default: high)
  --budget <usd>          Max budget in USD
  --runtime <min>         Max runtime in minutes
  --turns <n>             Max conversation turns
  --pr                    Create a pull request
  --pr-title <title>      PR title
  --pr-body <body>        PR body
  --claude-md <text>      Extra CLAUDE.md content to inject
  --context <text>        Additional context for the prompt
  --self-review           Enable self-review after PR creation
  --env <KEY=VALUE>       Environment variable (can repeat)
USAGE
    exit 1
}

if [ $# -lt 2 ]; then
    usage
fi

REPO_URL="$1"
PROMPT="$2"
shift 2

# Defaults
BRANCH=""
TARGET_BRANCH=""
MODEL="claude-opus-4-6"
EFFORT="high"
BUDGET=""
RUNTIME=""
TURNS=""
CREATE_PR=true
SELF_REVIEW=false
PR_TITLE=""
PR_BODY=""
CLAUDE_MD=""
CONTEXT=""
declare -a ENV_VARS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --branch)       BRANCH="$2"; shift 2 ;;
        --target-branch) TARGET_BRANCH="$2"; shift 2 ;;
        --model)        MODEL="$2"; shift 2 ;;
        --effort)       EFFORT="$2"; shift 2 ;;
        --budget)       BUDGET="$2"; shift 2 ;;
        --runtime)      RUNTIME="$2"; shift 2 ;;
        --turns)        TURNS="$2"; shift 2 ;;
        --pr)           CREATE_PR=true; shift ;;
        --self-review)  SELF_REVIEW=true; shift ;;
        --pr-title)     PR_TITLE="$2"; shift 2 ;;
        --pr-body)      PR_BODY="$2"; shift 2 ;;
        --claude-md)    CLAUDE_MD="$2"; shift 2 ;;
        --context)      CONTEXT="$2"; shift 2 ;;
        --env)          ENV_VARS+=("$2"); shift 2 ;;
        *)              echo "Unknown option: $1"; usage ;;
    esac
done

# Build JSON payload
JSON=$(jq -n \
    --arg repo_url "$REPO_URL" \
    --arg prompt "$PROMPT" \
    --arg branch "$BRANCH" \
    --arg target_branch "$TARGET_BRANCH" \
    --arg model "$MODEL" \
    --arg effort "$EFFORT" \
    --arg budget "$BUDGET" \
    --arg runtime "$RUNTIME" \
    --arg turns "$TURNS" \
    --argjson create_pr "$CREATE_PR" \
    --argjson self_review "$SELF_REVIEW" \
    --arg pr_title "$PR_TITLE" \
    --arg pr_body "$PR_BODY" \
    --arg claude_md "$CLAUDE_MD" \
    --arg context "$CONTEXT" \
    '{
        repo_url: $repo_url,
        prompt: $prompt,
        create_pr: $create_pr,
        self_review: $self_review
    }
    + if $branch != "" then {branch: $branch} else {} end
    + if $target_branch != "" then {target_branch: $target_branch} else {} end
    + if $model != "" then {model: $model} else {} end
    + if $effort != "" then {effort: $effort} else {} end
    + if $budget != "" then {max_budget_usd: ($budget | tonumber)} else {} end
    + if $runtime != "" then {max_runtime_min: ($runtime | tonumber)} else {} end
    + if $turns != "" then {max_turns: ($turns | tonumber)} else {} end
    + if $pr_title != "" then {pr_title: $pr_title} else {} end
    + if $pr_body != "" then {pr_body: $pr_body} else {} end
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
    echo "  # Stream logs (last 50 lines)"
    echo "  curl -s '${BACKFLOW_URL}/api/v1/tasks/${TASK_ID}/logs?tail=50'"
    echo ""
    echo "  # Cancel task"
    echo "  curl -s -X DELETE ${BACKFLOW_URL}/api/v1/tasks/${TASK_ID} | jq ."
else
    echo "Error (HTTP $HTTP_CODE):" >&2
    echo "$BODY" | jq . 2>/dev/null || echo "$BODY" >&2
    exit 1
fi
