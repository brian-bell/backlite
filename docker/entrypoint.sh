#!/usr/bin/env bash
set -euo pipefail

# --- Configuration from environment ---
REPO_URL="${REPO_URL:?REPO_URL is required}"
PROMPT="${PROMPT:?PROMPT is required}"
AUTH_MODE="${AUTH_MODE:-api_key}"
BRANCH="${BRANCH:-backflow/${TASK_ID:-$(date +%s)}}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"
MODEL="${MODEL:-claude-sonnet-4-6}"
MAX_BUDGET_USD="${MAX_BUDGET_USD:-10}"
MAX_TURNS="${MAX_TURNS:-200}"
CREATE_PR="${CREATE_PR:-false}"
PR_TITLE="${PR_TITLE:-}"
PR_BODY="${PR_BODY:-}"
CLAUDE_MD="${CLAUDE_MD:-}"
TASK_CONTEXT="${TASK_CONTEXT:-}"
MAX_RETRIES="${MAX_RETRIES:-3}"

WORKSPACE="/home/agent/workspace"
STATUS_FILE="${WORKSPACE}/status.json"

write_status() {
    local exit_code="$1"
    local complete="$2"
    local needs_input="$3"
    local question="$4"
    local error_msg="$5"

    cat > "$STATUS_FILE" <<STATUSEOF
{
  "exit_code": ${exit_code},
  "complete": ${complete},
  "needs_input": ${needs_input},
  "question": $(echo "$question" | jq -R .),
  "error": $(echo "$error_msg" | jq -R .)
}
STATUSEOF
}

# --- Clone ---
echo "==> Cloning ${REPO_URL} (depth 50)..."
git clone --depth 50 "$REPO_URL" "$WORKSPACE"
cd "$WORKSPACE"

# Checkout target branch if it's not the default
git fetch origin "$TARGET_BRANCH" 2>/dev/null || true
git checkout "$TARGET_BRANCH" 2>/dev/null || true

# Create working branch
git checkout -b "$BRANCH"
echo "==> Working on branch: ${BRANCH}"

# --- Inject CLAUDE.md ---
if [ -n "$CLAUDE_MD" ]; then
    echo "==> Injecting CLAUDE.md content..."
    if [ -f CLAUDE.md ]; then
        echo "" >> CLAUDE.md
        echo "$CLAUDE_MD" >> CLAUDE.md
    else
        echo "$CLAUDE_MD" > CLAUDE.md
    fi
fi

# --- Build prompt ---
FULL_PROMPT="$PROMPT"
if [ -n "$TASK_CONTEXT" ]; then
    FULL_PROMPT="Context: ${TASK_CONTEXT}

${FULL_PROMPT}"
fi

# --- GitHub auth ---
if [ -n "${GITHUB_TOKEN:-}" ]; then
    echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
fi

# --- Auth mode setup ---
echo "==> Auth mode: ${AUTH_MODE}"
if [ "$AUTH_MODE" = "api_key" ]; then
    if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
        echo "ERROR: ANTHROPIC_API_KEY is required in api_key mode" >&2
        exit 1
    fi
elif [ "$AUTH_MODE" = "max_subscription" ]; then
    if [ ! -d "$HOME/.claude" ]; then
        echo "ERROR: ~/.claude credentials not mounted (required for max_subscription mode)" >&2
        exit 1
    fi
    echo "==> Using Max subscription credentials from ~/.claude"
else
    echo "ERROR: Unknown AUTH_MODE: ${AUTH_MODE}" >&2
    exit 1
fi

# --- Build claude command args ---
CLAUDE_ARGS=(
    -p "$FULL_PROMPT"
    --dangerously-skip-permissions
    --model "$MODEL"
    --max-turns "$MAX_TURNS"
    --output-format json
    --verbose
)

# --max-budget-usd only applies to API key mode (billed per token)
if [ "$AUTH_MODE" = "api_key" ]; then
    CLAUDE_ARGS+=(--max-budget-usd "$MAX_BUDGET_USD")
fi

# --- Run Claude Code with retries ---
ATTEMPT=0
CLAUDE_EXIT=1
CLAUDE_OUTPUT=""

while [ $ATTEMPT -lt "$MAX_RETRIES" ]; do
    ATTEMPT=$((ATTEMPT + 1))
    echo "==> Running Claude Code (attempt ${ATTEMPT}/${MAX_RETRIES})..."

    set +e
    CLAUDE_OUTPUT=$(claude "${CLAUDE_ARGS[@]}" 2>&1)
    CLAUDE_EXIT=$?
    set -e

    if [ $CLAUDE_EXIT -eq 0 ]; then
        echo "==> Claude Code completed successfully"
        break
    fi

    echo "==> Claude Code exited with code ${CLAUDE_EXIT} (attempt ${ATTEMPT})"

    if [ $ATTEMPT -lt "$MAX_RETRIES" ]; then
        # Add error context to prompt for retry
        ERROR_TAIL=$(echo "$CLAUDE_OUTPUT" | tail -20)
        FULL_PROMPT="Previous attempt failed with error:
${ERROR_TAIL}

Please try again:
${PROMPT}"
        # Rebuild args with updated prompt
        CLAUDE_ARGS=( -p "$FULL_PROMPT" --dangerously-skip-permissions --model "$MODEL" --max-turns "$MAX_TURNS" --output-format json --verbose )
        if [ "$AUTH_MODE" = "api_key" ]; then
            CLAUDE_ARGS+=(--max-budget-usd "$MAX_BUDGET_USD")
        fi
    fi
done

# --- Detect needs_input ---
NEEDS_INPUT=false
QUESTION=""

if echo "$CLAUDE_OUTPUT" | jq -e '.result' 2>/dev/null | grep -qi 'question\|decision\|should I\|which approach\|do you want\|please clarify'; then
    NEEDS_INPUT=true
    QUESTION=$(echo "$CLAUDE_OUTPUT" | jq -r '.result // empty' 2>/dev/null | tail -5)
fi

# If claude ran out of turns without completing
if echo "$CLAUDE_OUTPUT" | jq -e '.is_error' 2>/dev/null | grep -q 'true'; then
    if echo "$CLAUDE_OUTPUT" | jq -r '.error_type // empty' 2>/dev/null | grep -q 'max_turns'; then
        NEEDS_INPUT=true
        QUESTION="Agent ran out of turns (${MAX_TURNS}) without completing the task"
    fi
fi

# --- Write status ---
COMPLETE=false
if [ $CLAUDE_EXIT -eq 0 ]; then
    COMPLETE=true
fi
ERROR_MSG=""
if [ $CLAUDE_EXIT -ne 0 ]; then
    ERROR_MSG=$(echo "$CLAUDE_OUTPUT" | tail -5)
fi
write_status "$CLAUDE_EXIT" "$COMPLETE" "$NEEDS_INPUT" "$QUESTION" "$ERROR_MSG"

# --- Commit remaining changes ---
echo "==> Committing any remaining changes..."
git add -A
if ! git diff --cached --quiet; then
    COMMIT_MSG="backflow: agent work on task ${TASK_ID:-unknown}

Automated commit by backflow agent.
Model: ${MODEL}"
    if [ "$AUTH_MODE" = "api_key" ]; then
        COMMIT_MSG="${COMMIT_MSG}
Budget: \$${MAX_BUDGET_USD}"
    fi
    git commit -m "$COMMIT_MSG"
fi

# --- Push ---
echo "==> Pushing branch ${BRANCH}..."
git push origin "$BRANCH" --force-with-lease 2>/dev/null || git push origin "$BRANCH"

# --- Create PR ---
if [ "$CREATE_PR" = "true" ]; then
    echo "==> Creating pull request..."
    PR_TITLE_FINAL="${PR_TITLE:-[backflow] ${PROMPT:0:60}}"

    if [ -z "$PR_BODY" ]; then
        PR_BODY="## Automated PR by Backflow

**Task:** ${PROMPT}

**Model:** ${MODEL}"
        if [ "$AUTH_MODE" = "api_key" ]; then
            PR_BODY="${PR_BODY}
**Budget:** \$${MAX_BUDGET_USD}"
        else
            PR_BODY="${PR_BODY}
**Auth:** Max subscription"
        fi
        PR_BODY="${PR_BODY}

---
*Created by [backflow](https://github.com/backflow-labs/backflow) agent*"
    fi

    PR_URL=$(gh pr create \
        --title "$PR_TITLE_FINAL" \
        --body "$PR_BODY" \
        --base "$TARGET_BRANCH" \
        --head "$BRANCH" 2>&1) || true

    if [ -n "$PR_URL" ]; then
        echo "==> PR created: ${PR_URL}"
    fi
fi

echo "==> Done (exit code: ${CLAUDE_EXIT})"
exit $CLAUDE_EXIT
