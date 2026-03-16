#!/usr/bin/env bash
set -euo pipefail

# --- Configuration from environment ---
REPO_URL="${REPO_URL:?REPO_URL is required}"
TASK_MODE="${TASK_MODE:-code}"
HARNESS="${HARNESS:-claude_code}"
AUTH_MODE="${AUTH_MODE:-api_key}"
BRANCH="${BRANCH:-backflow/${TASK_ID:-$(date +%s)}}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"
REVIEW_PR_NUMBER="${REVIEW_PR_NUMBER:-0}"
PROMPT="${PROMPT:-}"
MODEL="${MODEL:-claude-sonnet-4-6}"
EFFORT="${EFFORT:-high}"
MAX_BUDGET_USD="${MAX_BUDGET_USD:-10}"
MAX_TURNS="${MAX_TURNS:-200}"
CREATE_PR="${CREATE_PR:-false}"
PR_TITLE="${PR_TITLE:-}"
PR_BODY="${PR_BODY:-}"
CLAUDE_MD="${CLAUDE_MD:-}"
TASK_CONTEXT="${TASK_CONTEXT:-}"
SELF_REVIEW="${SELF_REVIEW:-false}"
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

# --- GitHub auth ---
if [ -n "${GITHUB_TOKEN:-}" ]; then
    echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
fi

# --- Auth mode setup ---
echo "==> Harness: ${HARNESS}, auth mode: ${AUTH_MODE}"
echo "==> Model: ${MODEL}, effort: ${EFFORT}"
if [ "$HARNESS" = "codex" ]; then
    if [ -z "${OPENAI_API_KEY:-}" ]; then
        echo "ERROR: OPENAI_API_KEY is required for codex harness" >&2
        exit 1
    fi
    echo "==> Logging in to codex with API key..."
    echo "$OPENAI_API_KEY" | codex login --with-api-key
elif [ "$AUTH_MODE" = "api_key" ]; then
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

# =============================================================================
# REVIEW MODE — review an existing PR and post feedback
# =============================================================================
if [ "$TASK_MODE" = "review" ]; then
    echo "==> PR Review mode: reviewing PR #${REVIEW_PR_NUMBER}"

    # Clone repo (needed for gh CLI context)
    echo "==> Cloning ${REPO_URL} (depth 1)..."
    git clone --depth 1 "$REPO_URL" "$WORKSPACE"
    cd "$WORKSPACE"

    # Build review prompt
    REVIEW_PROMPT="${PROMPT}"
    if [ -z "$REVIEW_PROMPT" ]; then
        REVIEW_PROMPT="Review this pull request thoroughly and provide constructive feedback."
    fi

    if [ -n "$TASK_CONTEXT" ]; then
        REVIEW_PROMPT="Context: ${TASK_CONTEXT}

${REVIEW_PROMPT}"
    fi

    FULL_REVIEW_PROMPT="You are reviewing pull request #${REVIEW_PR_NUMBER} in this repository.

${REVIEW_PROMPT}

Steps:
1. Read the PR description: gh pr view ${REVIEW_PR_NUMBER}
2. Read the full diff: gh pr diff ${REVIEW_PR_NUMBER}
3. If needed, check out the PR branch to inspect specific files: gh pr checkout ${REVIEW_PR_NUMBER}
4. Post your review using: gh pr review ${REVIEW_PR_NUMBER} --comment --body '<your review>'

Focus on:
- Bugs and logic errors
- Security issues
- Missing error handling that could cause failures
- Correctness of the implementation vs the PR description
- Edge cases and potential regressions

Do NOT comment on:
- Code style or formatting preferences
- Minor naming preferences
- Things that are working correctly

You MUST post your review as a comment on the PR using the gh CLI. Do not just print your review to stdout."

    # Inject CLAUDE.md if provided
    if [ -n "$CLAUDE_MD" ]; then
        echo "==> Injecting CLAUDE.md content..."
        if [ -f CLAUDE.md ]; then
            echo "" >> CLAUDE.md
            echo "$CLAUDE_MD" >> CLAUDE.md
        else
            echo "$CLAUDE_MD" > CLAUDE.md
        fi
    fi

    CLAUDE_ARGS=(
        -p "$FULL_REVIEW_PROMPT"
        --dangerously-skip-permissions
        --model "$MODEL"
        --effort "$EFFORT"
        --max-turns "$MAX_TURNS"
        --output-format stream-json
        --verbose
    )
    if [ "$AUTH_MODE" = "api_key" ]; then
        CLAUDE_ARGS+=(--max-budget-usd "$MAX_BUDGET_USD")
    fi

    CLAUDE_LOG="${WORKSPACE}/claude_output.log"
    set +e
    claude "${CLAUDE_ARGS[@]}" 2>&1 | tee "$CLAUDE_LOG"
    CLAUDE_EXIT=${PIPESTATUS[0]}
    CLAUDE_OUTPUT=$(cat "$CLAUDE_LOG")
    set -e

    COMPLETE=false
    ERROR_MSG=""
    if [ $CLAUDE_EXIT -eq 0 ]; then
        COMPLETE=true
        echo "==> PR review completed successfully"
    else
        ERROR_MSG=$(echo "$CLAUDE_OUTPUT" | tail -5)
        echo "==> PR review failed (exit code: ${CLAUDE_EXIT})"
    fi

    # Look up PR URL for status
    PR_URL=$(gh pr view "$REVIEW_PR_NUMBER" --json url --jq '.url' 2>/dev/null || true)

    write_status "$CLAUDE_EXIT" "$COMPLETE" false "" "$ERROR_MSG" "$PR_URL"
    echo "==> Done (exit code: ${CLAUDE_EXIT})"
    exit $CLAUDE_EXIT
fi

# =============================================================================
# CODE MODE (default) — clone, code, commit, push, optionally create PR
# =============================================================================
if [ -z "$PROMPT" ]; then
    echo "ERROR: PROMPT is required in code mode" >&2
    exit 1
fi

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
    GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
3. Create a pull request using the gh CLI:
   - Base branch: ${TARGET_BRANCH}
   - Head branch: ${BRANCH}"

    if [ -n "$PR_TITLE" ]; then
        GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
   - Title: ${PR_TITLE}"
    else
        GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
   - Title: [backflow] <generate a concise, descriptive title based on the changes you made>"
    fi

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

# --- Build command args based on harness ---
echo "==> Harness: ${HARNESS}"

build_claude_args() {
    local prompt="$1"
    CLAUDE_ARGS=(
        -p "$prompt"
        --dangerously-skip-permissions
        --model "$MODEL"
        --effort "$EFFORT"
        --max-turns "$MAX_TURNS"
        --output-format stream-json
        --verbose
    )
    if [ "$AUTH_MODE" = "api_key" ]; then
        CLAUDE_ARGS+=(--max-budget-usd "$MAX_BUDGET_USD")
    fi
}

build_codex_args() {
    local prompt="$1"
    CODEX_ARGS=(
        exec
        --model "$MODEL"
        -c "model_reasoning_effort=${EFFORT}"
        --dangerously-bypass-approvals-and-sandbox
        "$prompt"
    )
}

# --- Run agent with retries ---
ATTEMPT=0
CLAUDE_EXIT=1
CLAUDE_OUTPUT=""

while [ $ATTEMPT -lt "$MAX_RETRIES" ]; do
    ATTEMPT=$((ATTEMPT + 1))
    echo "==> Running ${HARNESS} (attempt ${ATTEMPT}/${MAX_RETRIES})..."

    CLAUDE_LOG="${WORKSPACE}/claude_output.log"
    set +e
    if [ "$HARNESS" = "codex" ]; then
        build_codex_args "$FULL_PROMPT"
        codex "${CODEX_ARGS[@]}" 2>&1 | tee "$CLAUDE_LOG"
    else
        build_claude_args "$FULL_PROMPT"
        claude "${CLAUDE_ARGS[@]}" 2>&1 | tee "$CLAUDE_LOG"
    fi
    CLAUDE_EXIT=${PIPESTATUS[0]}
    CLAUDE_OUTPUT=$(cat "$CLAUDE_LOG")
    set -e

    if [ $CLAUDE_EXIT -eq 0 ]; then
        echo "==> ${HARNESS} completed successfully"
        break
    fi

    echo "==> ${HARNESS} exited with code ${CLAUDE_EXIT} (attempt ${ATTEMPT})"
    echo "$CLAUDE_OUTPUT" | tail -30

    if [ $ATTEMPT -lt "$MAX_RETRIES" ]; then
        # Add error context to prompt for retry
        ERROR_TAIL=$(echo "$CLAUDE_OUTPUT" | tail -20)
        FULL_PROMPT="Previous attempt failed with error:
${ERROR_TAIL}

Please try again:
${PROMPT}${GIT_INSTRUCTIONS}"
    fi
done

# --- Parse result ---
NEEDS_INPUT=false
QUESTION=""

if [ "$HARNESS" = "codex" ]; then
    # Codex outputs plain text; scan for question indicators
    if echo "$CLAUDE_OUTPUT" | grep -qi 'question\|decision\|should I\|which approach\|do you want\|please clarify'; then
        NEEDS_INPUT=true
        QUESTION=$(echo "$CLAUDE_OUTPUT" | tail -5)
    fi
else
    # stream-json emits one JSON object per line; the final "result" message has the outcome.
    RESULT_LINE=$(echo "$CLAUDE_OUTPUT" | grep '"type":"result"' | tail -1 || true)
    if [ -n "$RESULT_LINE" ]; then
        RESULT_TEXT=$(echo "$RESULT_LINE" | jq -r '.result // empty' 2>/dev/null || true)
        if echo "$RESULT_TEXT" | grep -qi 'question\|decision\|should I\|which approach\|do you want\|please clarify'; then
            NEEDS_INPUT=true
            QUESTION=$(echo "$RESULT_TEXT" | tail -5)
        fi

        # If claude ran out of turns without completing
        IS_ERROR=$(echo "$RESULT_LINE" | jq -r '.is_error // false' 2>/dev/null || true)
        if [ "$IS_ERROR" = "true" ]; then
            ERROR_TYPE=$(echo "$RESULT_LINE" | jq -r '.error_type // empty' 2>/dev/null || true)
            if [ "$ERROR_TYPE" = "max_turns" ]; then
                NEEDS_INPUT=true
                QUESTION="Agent ran out of turns (${MAX_TURNS}) without completing the task"
            fi
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

# --- Comment prompt and metadata on PR ---
if [ -n "$PR_URL" ]; then
    echo "==> Commenting task info on PR..."

    # Extract actual cost from agent output (stream-json result line)
    ACTUAL_COST=$(grep '"type":"result"' "$CLAUDE_LOG" 2>/dev/null | tail -1 | jq -r '.total_cost_usd // empty' 2>/dev/null || true)

    # Format prompt as markdown quote
    QUOTED_PROMPT=$(echo "$PROMPT" | sed 's/^/> /')

    COMMENT_BODY="## Backflow Task

${QUOTED_PROMPT}

| Field | Value |
|-------|-------|
| Task ID | \`${TASK_ID:-unknown}\` |
| Harness | \`${HARNESS}\` |
| Model | \`${MODEL}\` |
| Effort | \`${EFFORT}\` |
| Max Budget | \`\$${MAX_BUDGET_USD}\` |"
    if [ -n "$ACTUAL_COST" ] && [ "$ACTUAL_COST" != "0" ]; then
        COMMENT_BODY="${COMMENT_BODY}
| Cost | \`\$${ACTUAL_COST}\` |"
    fi
    COMMENT_BODY="${COMMENT_BODY}
| Max Turns | \`${MAX_TURNS}\` |
| Auth Mode | \`${AUTH_MODE}\` |
| Attempts | \`${ATTEMPT}/${MAX_RETRIES}\` |"
    if [ "$SELF_REVIEW" = "true" ]; then
        COMMENT_BODY="${COMMENT_BODY}
| Self-Review | enabled |"
    fi

    gh pr comment "$PR_URL" --body "$COMMENT_BODY" 2>/dev/null || true
fi

# --- Self-review phase ---
if [ "$SELF_REVIEW" = "true" ] && [ -n "$PR_URL" ]; then
    echo "==> Starting self-review phase..."

    # Cap review budget at 20% of coding budget (minimum $2)
    REVIEW_BUDGET=$(echo "$MAX_BUDGET_USD" | awk '{b = $1 * 0.2; print (b < 2) ? 2 : b}')

    REVIEW_PROMPT="You are reviewing a pull request that you (a different instance) just created.

PR URL: ${PR_URL}

Review the PR by:
1. Read the full diff with: gh pr diff ${PR_URL}
2. Look at the PR description with: gh pr view ${PR_URL}
3. Post your review using: gh pr review ${PR_URL} --comment --body '<your review>'

Focus on:
- Bugs and logic errors
- Security issues
- Missing error handling that could cause failures
- Correctness of the implementation vs the PR description

Do NOT comment on:
- Code style or formatting
- Minor naming preferences
- Things that are working correctly

You MUST post your review as a comment on the PR using the gh CLI. Do not just print your review to stdout."

    REVIEW_LOG="${WORKSPACE}/review_output.log"
    set +e
    if [ "$HARNESS" = "codex" ]; then
        REVIEW_CODEX_ARGS=(
            exec
            --model "$MODEL"
            --dangerously-bypass-approvals-and-sandbox
            "$REVIEW_PROMPT"
        )
        codex "${REVIEW_CODEX_ARGS[@]}" 2>&1 | tee "$REVIEW_LOG"
    else
        REVIEW_ARGS=(
            -p "$REVIEW_PROMPT"
            --dangerously-skip-permissions
            --model "$MODEL"
            --max-turns 10
            --output-format stream-json
            --verbose
        )
        if [ "$AUTH_MODE" = "api_key" ]; then
            REVIEW_ARGS+=(--max-budget-usd "$REVIEW_BUDGET")
        fi
        claude "${REVIEW_ARGS[@]}" 2>&1 | tee "$REVIEW_LOG"
    fi
    REVIEW_EXIT=${PIPESTATUS[0]}
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
