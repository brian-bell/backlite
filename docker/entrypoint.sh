#!/usr/bin/env bash
set -euo pipefail

# --- Configuration from environment ---
REPO_URL="${REPO_URL:?REPO_URL is required}"
PROMPT="${PROMPT:?PROMPT is required}"
AUTH_MODE="${AUTH_MODE:-api_key}"
BRANCH="${BRANCH:-backflow/${TASK_ID:-$(date +%s)}}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"
MODEL="${MODEL:-claude-sonnet-4-6}"
EFFORT="${EFFORT:-high}"
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
    local pr_url="${6:-}"

    cat > "$STATUS_FILE" <<STATUSEOF
{
  "exit_code": ${exit_code},
  "complete": ${complete},
  "needs_input": ${needs_input},
  "question": $(echo "$question" | jq -R .),
  "error": $(echo "$error_msg" | jq -R .),
  "pr_url": $(echo "$pr_url" | jq -R .)
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

# Always instruct the agent to commit and push
GIT_INSTRUCTIONS="

After completing the coding task, you MUST do the following git operations:

1. Stage and commit all your changes with a descriptive commit message.
2. Push the branch '${BRANCH}' to origin."

# Append PR creation instructions if requested
if [ "$CREATE_PR" = "true" ]; then
    PR_TITLE_FINAL="${PR_TITLE:-[backflow] ${PROMPT:0:60}}"
    GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
3. Create a pull request using the gh CLI:
   - Base branch: ${TARGET_BRANCH}
   - Head branch: ${BRANCH}
   - Title: ${PR_TITLE_FINAL}"

    if [ -n "$PR_BODY" ]; then
        GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
   - Body: ${PR_BODY}"
    else
        GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
   - Write a clear PR description summarizing what you changed and why."
    fi

    GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}

Do NOT skip the PR creation step. The PR must exist on GitHub when you are done."
fi

FULL_PROMPT="${FULL_PROMPT}${GIT_INSTRUCTIONS}"

# --- GitHub auth ---
if [ -n "${GITHUB_TOKEN:-}" ]; then
    echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
fi

# --- Auth mode setup ---
echo "==> Auth mode: ${AUTH_MODE}"
echo "==> Model: ${MODEL}, effort: ${EFFORT}"
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
    --effort "$EFFORT"
    --max-turns "$MAX_TURNS"
    --output-format stream-json
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

    CLAUDE_LOG="${WORKSPACE}/claude_output.log"
    set +e
    claude "${CLAUDE_ARGS[@]}" 2>&1 | tee "$CLAUDE_LOG"
    CLAUDE_EXIT=${PIPESTATUS[0]}
    CLAUDE_OUTPUT=$(cat "$CLAUDE_LOG")
    set -e

    if [ $CLAUDE_EXIT -eq 0 ]; then
        echo "==> Claude Code completed successfully"
        break
    fi

    echo "==> Claude Code exited with code ${CLAUDE_EXIT} (attempt ${ATTEMPT})"
    echo "$CLAUDE_OUTPUT" | tail -30

    if [ $ATTEMPT -lt "$MAX_RETRIES" ]; then
        # Add error context to prompt for retry
        ERROR_TAIL=$(echo "$CLAUDE_OUTPUT" | tail -20)
        FULL_PROMPT="Previous attempt failed with error:
${ERROR_TAIL}

Please try again:
${PROMPT}${GIT_INSTRUCTIONS}"
        # Rebuild args with updated prompt
        CLAUDE_ARGS=( -p "$FULL_PROMPT" --dangerously-skip-permissions --model "$MODEL" --effort "$EFFORT" --max-turns "$MAX_TURNS" --output-format stream-json --verbose )
        if [ "$AUTH_MODE" = "api_key" ]; then
            CLAUDE_ARGS+=(--max-budget-usd "$MAX_BUDGET_USD")
        fi
    fi
done

# --- Parse result from stream-json output ---
# stream-json emits one JSON object per line; the final "result" message has the outcome.
RESULT_LINE=$(echo "$CLAUDE_OUTPUT" | grep '"type":"result"' | tail -1)
NEEDS_INPUT=false
QUESTION=""

if [ -n "$RESULT_LINE" ]; then
    RESULT_TEXT=$(echo "$RESULT_LINE" | jq -r '.result // empty' 2>/dev/null)
    if echo "$RESULT_TEXT" | grep -qi 'question\|decision\|should I\|which approach\|do you want\|please clarify'; then
        NEEDS_INPUT=true
        QUESTION=$(echo "$RESULT_TEXT" | tail -5)
    fi

    # If claude ran out of turns without completing
    IS_ERROR=$(echo "$RESULT_LINE" | jq -r '.is_error // false' 2>/dev/null)
    if [ "$IS_ERROR" = "true" ]; then
        ERROR_TYPE=$(echo "$RESULT_LINE" | jq -r '.error_type // empty' 2>/dev/null)
        if [ "$ERROR_TYPE" = "max_turns" ]; then
            NEEDS_INPUT=true
            QUESTION="Agent ran out of turns (${MAX_TURNS}) without completing the task"
        fi
    fi
fi

# --- Determine completion status ---
COMPLETE=false
if [ $CLAUDE_EXIT -eq 0 ]; then
    COMPLETE=true
fi
ERROR_MSG=""
if [ $CLAUDE_EXIT -ne 0 ]; then
    ERROR_MSG=$(echo "$CLAUDE_OUTPUT" | tail -5)
fi

# --- Extract PR URL ---
PR_URL=""
if [ "$CREATE_PR" = "true" ] && [ "$COMPLETE" = "true" ]; then
    echo "==> Looking up PR URL..."
    PR_URL=$(gh pr list --head "$BRANCH" --base "$TARGET_BRANCH" --json url --jq '.[0].url' 2>/dev/null || true)
    if [ -n "$PR_URL" ]; then
        echo "==> PR found: ${PR_URL}"
    else
        echo "==> No PR found for branch ${BRANCH}"
    fi
fi

# --- Self-review phase ---
if [ -n "$PR_URL" ]; then
    echo "==> Starting self-review phase..."

    # Cap review budget at 20% of coding budget (minimum $2)
    REVIEW_BUDGET=$(echo "$MAX_BUDGET_USD" | awk '{b = $1 * 0.2; print (b < 2) ? 2 : b}')

    REVIEW_PROMPT="You are reviewing a pull request that you (a different instance) just created.

PR URL: ${PR_URL}

Review the PR by:
1. Read the full diff with: gh pr diff ${PR_URL}
2. Look at the PR description with: gh pr view ${PR_URL}
3. Post a review using: gh pr review ${PR_URL} --approve, --request-changes, or --comment
   Include a body with your review summarizing your findings.

Focus on:
- Bugs and logic errors
- Security issues
- Missing error handling that could cause failures
- Correctness of the implementation vs the PR description

Do NOT comment on:
- Code style or formatting
- Minor naming preferences
- Things that are working correctly

If everything looks good, approve the PR. If there are real issues, request changes and explain what needs fixing."

    REVIEW_ARGS=(
        -p "$REVIEW_PROMPT"
        --dangerously-skip-permissions
        --model "$MODEL"
        --max-turns 3
        --output-format json
        --verbose
    )
    if [ "$AUTH_MODE" = "api_key" ]; then
        REVIEW_ARGS+=(--max-budget-usd "$REVIEW_BUDGET")
    fi

    set +e
    REVIEW_OUTPUT=$(claude "${REVIEW_ARGS[@]}" 2>&1)
    REVIEW_EXIT=$?
    set -e

    if [ $REVIEW_EXIT -eq 0 ]; then
        echo "==> Self-review completed successfully"
    else
        echo "==> Self-review failed (exit code: ${REVIEW_EXIT}), continuing anyway"
    fi
fi

# --- Write status ---
write_status "$CLAUDE_EXIT" "$COMPLETE" "$NEEDS_INPUT" "$QUESTION" "$ERROR_MSG" "$PR_URL"

echo "==> Done (exit code: ${CLAUDE_EXIT})"
exit $CLAUDE_EXIT
