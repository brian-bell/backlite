#!/usr/bin/env bash
set -euo pipefail

# --- Configuration from environment ---
PROMPT="${PROMPT:?PROMPT is required}"
HARNESS="${HARNESS:-claude_code}"
MODEL="${MODEL:-claude-sonnet-4-6}"
EFFORT="${EFFORT:-medium}"
MAX_BUDGET_USD="${MAX_BUDGET_USD:-10}"
MAX_TURNS="${MAX_TURNS:-200}"
CREATE_PR="${CREATE_PR:-false}"
PR_TITLE="${PR_TITLE:-}"
PR_BODY="${PR_BODY:-}"
CLAUDE_MD="${CLAUDE_MD:-}"
TASK_CONTEXT="${TASK_CONTEXT:-}"
SELF_REVIEW="${SELF_REVIEW:-false}"
MAX_RETRIES="${MAX_RETRIES:-3}"

# --- Download env vars offloaded to S3 (for large prompts/context) ---
fetch_s3_var() {
    local url_var="$1"
    local target_var="$2"
    local url="${!url_var:-}"
    if [ -n "$url" ]; then
        echo "==> Downloading ${target_var} from S3..."
        local content
        content=$(curl -fsSL "$url") || { echo "ERROR: Failed to download ${target_var} from S3" >&2; exit 1; }
        printf -v "$target_var" '%s' "$content"
        export "$target_var"
    fi
}

fetch_s3_var "PROMPT_S3_URL" "PROMPT"
fetch_s3_var "CLAUDE_MD_S3_URL" "CLAUDE_MD"
fetch_s3_var "TASK_CONTEXT_S3_URL" "TASK_CONTEXT"
fetch_s3_var "PR_BODY_S3_URL" "PR_BODY"

WORKSPACE="/home/agent/workspace"
mkdir -p "$WORKSPACE"
STATUS_FILE="${WORKSPACE}/status.json"
START_TIME=$(date +%s)

source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/status_writer.sh"

# --- GitHub auth ---
if [ -n "${GITHUB_TOKEN:-}" ]; then
    echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
fi

# --- Auth setup ---
echo "==> Harness: ${HARNESS}"
echo "==> Model: ${MODEL}, effort: ${EFFORT}"
if [ "$HARNESS" = "codex" ]; then
    if [ -z "${OPENAI_API_KEY:-}" ]; then
        echo "ERROR: OPENAI_API_KEY is required for codex harness" >&2
        exit 1
    fi
    echo "==> Logging in to codex with API key..."
    echo "$OPENAI_API_KEY" | codex login --with-api-key
elif [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "ERROR: ANTHROPIC_API_KEY is required" >&2
    exit 1
fi

# =============================================================================
# PREP STAGE — infer repo_url, target_branch, task_type from the prompt
# =============================================================================

echo "==> Running prep stage..."

PREP_PROMPT="Analyze this task prompt and extract structured information.

Output ONLY a JSON object with these fields:
- repo_url: the GitHub repository HTTPS URL (required — look for github.com URLs)
- target_branch: the base branch to work from (default: \"main\")
- task_type: either \"code\" (implement changes, fix bugs, add features) or \"review\" (review an existing PR)
- pr_url: the full PR URL (only if task_type is \"review\")

Rules for determining task_type:
- If the prompt contains a PR URL (github.com/owner/repo/pull/NUMBER), it's a review task
- If the prompt contains an issue URL, a repo URL, or describes code changes, it's a code task
- If ambiguous (e.g. \"fix the issues in PR #42\"), prefer code — the user wants changes made
- GitHub issue URLs (github.com/owner/repo/issues/NUMBER) are code tasks, not review tasks

Rules for repo_url:
- Extract from any GitHub URL in the prompt (repo URL, PR URL, issue URL)
- Always use HTTPS format: https://github.com/owner/repo
- Strip any path beyond owner/repo (no /pull/N, /issues/N, etc.)

Output ONLY valid JSON. No markdown, no explanation, no code fences.

User prompt:
${PROMPT}"

PREP_LOG="/tmp/prep_output.log"
set +e
if [ "$HARNESS" = "codex" ]; then
    codex exec \
        --model "$MODEL" \
        --dangerously-bypass-approvals-and-sandbox \
        "$PREP_PROMPT" 2>&1 | tee "$PREP_LOG"
else
    claude -p "$PREP_PROMPT" \
        --model claude-haiku-4-5-20251001 \
        --max-turns 1 \
        --output-format stream-json \
        --verbose 2>&1 | tee "$PREP_LOG"
fi
PREP_EXIT=${PIPESTATUS[0]}
set -e

if [ $PREP_EXIT -ne 0 ]; then
    ELAPSED_SEC=$(( $(date +%s) - START_TIME ))
    write_status "$PREP_EXIT" false false "" "Prep stage failed (exit code: ${PREP_EXIT})" "" "0" "$ELAPSED_SEC" "" "" ""
    exit 1
fi

# Extract the result text — stream-json for claude, plain text for codex
if [ "$HARNESS" = "codex" ]; then
    # Codex CLI prints a banner before the model response and token stats
    # after it.  Strip markdown fences, then extract the first balanced
    # JSON object (track brace depth, stop when braces balance).
    PREP_RESULT=$(sed '/^```/d' "$PREP_LOG" | awk '
        /^\s*\{/ && !started { started=1 }
        started {
            print
            for (i=1; i<=length($0); i++) {
                c = substr($0, i, 1)
                if (c == "{") depth++
                else if (c == "}") depth--
            }
            if (depth == 0) exit
        }')
else
    PREP_RESULT=$(grep '"type":"result"' "$PREP_LOG" | tail -1 | jq -r '.result // empty' 2>/dev/null || true)
fi
if [ -z "$PREP_RESULT" ]; then
    ELAPSED_SEC=$(( $(date +%s) - START_TIME ))
    write_status "1" false false "" "Prep stage produced no result" "" "0" "$ELAPSED_SEC" "" "" ""
    exit 1
fi

# Strip markdown code fences if present (e.g. ```json ... ```)
PREP_RESULT=$(echo "$PREP_RESULT" | sed '/^```/d')

# The result text should be JSON — write it to prep.json
echo "$PREP_RESULT" | jq . > /tmp/prep.json 2>/dev/null || {
    ELAPSED_SEC=$(( $(date +%s) - START_TIME ))
    write_status "1" false false "" "Prep stage output is not valid JSON: ${PREP_RESULT:0:200}" "" "0" "$ELAPSED_SEC" "" "" ""
    exit 1
}

REPO_URL=$(jq -r '.repo_url // empty' /tmp/prep.json)
TARGET_BRANCH=$(jq -r '.target_branch // "main"' /tmp/prep.json)
TASK_TYPE=$(jq -r '.task_type // "code"' /tmp/prep.json)
PR_URL=$(jq -r '.review_pr_url // .pr_url // empty' /tmp/prep.json)

echo "==> Prep result: repo=${REPO_URL} branch=${TARGET_BRANCH} type=${TASK_TYPE}"

if [ -z "$REPO_URL" ]; then
    ELAPSED_SEC=$(( $(date +%s) - START_TIME ))
    write_status "1" false false "" "Could not determine repository URL from the prompt. Include a GitHub URL in your message." "" "0" "$ELAPSED_SEC" "" "" "$TASK_TYPE"
    exit 1
fi

if [ -n "$PR_URL" ] && [ "$TASK_TYPE" = "review" ]; then
    echo "==> Review PR: ${PR_URL}"
fi

# =============================================================================
# CLONE
# =============================================================================
if [ "$TASK_TYPE" = "review" ]; then
    echo "==> Cloning ${REPO_URL} (depth 1 for review)..."
    git clone --depth 1 "$REPO_URL" "$WORKSPACE"
else
    echo "==> Cloning ${REPO_URL} (depth 50 for code)..."
    git clone --depth 50 "$REPO_URL" "$WORKSPACE"
fi
cd "$WORKSPACE"

if [ "$TASK_TYPE" = "code" ]; then
    git fetch origin "$TARGET_BRANCH" 2>/dev/null || true
    git checkout "$TARGET_BRANCH" 2>/dev/null || true
fi

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

# =============================================================================
# REVIEW MODE
# =============================================================================
if [ "$TASK_TYPE" = "review" ]; then
    echo "==> Review mode: reviewing ${PR_URL}"

    REVIEW_PROMPT="${PROMPT}"
    if [ -z "$REVIEW_PROMPT" ]; then
        REVIEW_PROMPT="Review this pull request thoroughly and provide constructive feedback."
    fi

    if [ -n "$TASK_CONTEXT" ]; then
        REVIEW_PROMPT="Context: ${TASK_CONTEXT}

${REVIEW_PROMPT}"
    fi

    PR_REF="${PR_URL}"

    FULL_REVIEW_PROMPT="You are reviewing pull request ${PR_URL} in this repository.

${REVIEW_PROMPT}

Steps:
1. Read the PR description: gh pr view ${PR_REF}
2. Read the full diff: gh pr diff ${PR_REF}
3. If needed, check out the PR branch to inspect specific files: gh pr checkout ${PR_REF}
4. Post your review using: gh pr review ${PR_REF} --comment --body '<your review>'

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

    CLAUDE_LOG="/tmp/container_output.log"
    set +e
    if [ "$HARNESS" = "codex" ]; then
        CODEX_REVIEW_ARGS=(
            exec
            --model "$MODEL"
            -c "model_reasoning_effort=${EFFORT}"
            --dangerously-bypass-approvals-and-sandbox
            "$FULL_REVIEW_PROMPT"
        )
        codex "${CODEX_REVIEW_ARGS[@]}" 2>&1 | tee "$CLAUDE_LOG"
    else
        CLAUDE_ARGS=(
            -p "$FULL_REVIEW_PROMPT"
            --dangerously-skip-permissions
            --model "$MODEL"
            --effort "$EFFORT"
            --max-turns "$MAX_TURNS"
            --output-format stream-json
            --verbose
            --max-budget-usd "$MAX_BUDGET_USD"
        )
        claude "${CLAUDE_ARGS[@]}" 2>&1 | tee "$CLAUDE_LOG"
    fi
    CLAUDE_EXIT=${PIPESTATUS[0]}
    CLAUDE_OUTPUT=$(cat "$CLAUDE_LOG")
    set -e

    COMPLETE=false
    ERROR_MSG=""
    if [ $CLAUDE_EXIT -eq 0 ]; then
        COMPLETE=true
        echo "==> PR review completed successfully"

        # Verify the review was actually posted to the PR
        echo "==> Verifying review was posted to PR..."
        START_ISO=$(date -u -d "@$START_TIME" +%Y-%m-%dT%H:%M:%SZ)
        REVIEWS_AFTER=$(gh pr view "$PR_REF" --json reviews \
            --jq '[.reviews[] | select(.submittedAt > "'"$START_ISO"'")] | length' 2>/dev/null || echo "0")
        COMMENTS_AFTER=$(gh pr view "$PR_REF" --json comments \
            --jq '[.comments[] | select(.createdAt > "'"$START_ISO"'")] | length' 2>/dev/null || echo "0")
        if [ "$((REVIEWS_AFTER + COMMENTS_AFTER))" -eq 0 ]; then
            COMPLETE=false
            CLAUDE_EXIT=1
            ERROR_MSG="Review completed but was not posted to the PR. Check that the GitHub token has permission to write pull request reviews."
            echo "==> WARNING: ${ERROR_MSG}"
        else
            echo "==> Verified: review posted (${REVIEWS_AFTER} reviews, ${COMMENTS_AFTER} comments since task start)"
        fi
    else
        ERROR_MSG=$(echo "$CLAUDE_OUTPUT" | tail -5)
        echo "==> PR review failed (exit code: ${CLAUDE_EXIT})"
    fi

    REVIEW_COST=$(grep '"type":"result"' "$CLAUDE_LOG" 2>/dev/null | tail -1 | jq -r '.total_cost_usd // 0' 2>/dev/null || echo "0")
    ELAPSED_SEC=$(( $(date +%s) - START_TIME ))
    write_status "$CLAUDE_EXIT" "$COMPLETE" false "" "$ERROR_MSG" "$PR_URL" "$REVIEW_COST" "$ELAPSED_SEC" "$REPO_URL" "$TARGET_BRANCH" "$TASK_TYPE"
    echo "==> Done (exit code: ${CLAUDE_EXIT})"
    exit $CLAUDE_EXIT
fi

# =============================================================================
# CODE MODE — code, commit, push, optionally create PR
# =============================================================================

# --- Build prompt ---
FULL_PROMPT="$PROMPT"
if [ -n "$TASK_CONTEXT" ]; then
    FULL_PROMPT="Context: ${TASK_CONTEXT}

${FULL_PROMPT}"
fi

# Instruct the agent to commit, push, and create a PR.
# The agent picks its own descriptive branch name.
GIT_INSTRUCTIONS="

After completing the coding task, you MUST do the following git operations:

1. Create a new branch with a descriptive name prefixed with 'backlite/' (e.g. backlite/fix-auth-bug).
2. Stage and commit all your changes with a descriptive commit message.
3. Push your branch to origin."

if [ "$CREATE_PR" = "true" ]; then
    GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
4. Create a pull request using the gh CLI:
   - Base branch: ${TARGET_BRANCH}
   - Head branch: your new backlite/ branch"

    if [ -n "$PR_TITLE" ]; then
        GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
   - Title: ${PR_TITLE}"
    else
        GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}
   - Title: [backlite] <generate a concise, descriptive title based on the changes you made>"
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

GIT_INSTRUCTIONS="${GIT_INSTRUCTIONS}

5. After all git operations are complete, write a JSON file to /tmp/code_result.json with:
   {\"branch\": \"<your-branch-name>\", \"pr_url\": \"<pr-url-or-empty>\"}
   This file MUST exist when you are done."

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
        --max-budget-usd "$MAX_BUDGET_USD"
    )
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

    CLAUDE_LOG="/tmp/container_output.log"
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
    if echo "$CLAUDE_OUTPUT" | grep -qi 'question\|decision\|should I\|which approach\|do you want\|please clarify'; then
        NEEDS_INPUT=true
        QUESTION=$(echo "$CLAUDE_OUTPUT" | tail -5)
    fi
else
    RESULT_LINE=$(echo "$CLAUDE_OUTPUT" | grep '"type":"result"' | tail -1 || true)
    if [ -n "$RESULT_LINE" ]; then
        RESULT_TEXT=$(echo "$RESULT_LINE" | jq -r '.result // empty' 2>/dev/null || true)
        if echo "$RESULT_TEXT" | grep -qi 'question\|decision\|should I\|which approach\|do you want\|please clarify'; then
            NEEDS_INPUT=true
            QUESTION=$(echo "$RESULT_TEXT" | tail -5)
        fi

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

# --- Extract cost from agent output ---
ACTUAL_COST="0"
if [ "$HARNESS" != "codex" ]; then
    ACTUAL_COST=$(grep '"type":"result"' "$CLAUDE_LOG" 2>/dev/null | tail -1 | jq -r '.total_cost_usd // 0' 2>/dev/null || echo "0")
fi

# --- Extract PR URL from code_result.json or gh CLI ---
PR_URL=""
CODE_BRANCH=""
if [ -f /tmp/code_result.json ]; then
    PR_URL=$(jq -r '.pr_url // empty' /tmp/code_result.json 2>/dev/null || true)
    CODE_BRANCH=$(jq -r '.branch // empty' /tmp/code_result.json 2>/dev/null || true)
    echo "==> code_result.json: branch=${CODE_BRANCH} pr_url=${PR_URL}"
elif [ "$COMPLETE" = "true" ]; then
    echo "==> WARNING: code_result.json not found, falling back to git"
fi

# Fallback: detect branch from git if code_result.json was missing or incomplete
if [ -z "$CODE_BRANCH" ] && [ "$COMPLETE" = "true" ]; then
    CODE_BRANCH=$(git -C "$WORKSPACE" branch --show-current 2>/dev/null || true)
    if [ -n "$CODE_BRANCH" ] && [ "$CODE_BRANCH" != "$TARGET_BRANCH" ]; then
        echo "==> Detected branch from git: ${CODE_BRANCH}"
    else
        CODE_BRANCH=""
    fi
fi

# Fallback: look up PR via gh CLI if agent created one
if [ -z "$PR_URL" ] && [ "$CREATE_PR" = "true" ] && [ "$COMPLETE" = "true" ]; then
    if [ -n "$CODE_BRANCH" ]; then
        echo "==> Looking up PR URL for branch ${CODE_BRANCH}..."
        PR_URL=$(gh pr list --head "$CODE_BRANCH" --base "$TARGET_BRANCH" --json url --jq '.[0].url' 2>/dev/null || true)
    fi
    if [ -n "$PR_URL" ]; then
        echo "==> PR found: ${PR_URL}"
    else
        echo "==> WARNING: task completed with create_pr=true but no PR found"
    fi
fi

# --- Comment prompt and metadata on PR ---
if [ -n "$PR_URL" ]; then
    echo "==> Commenting task info on PR..."

    END_TIME=$(date +%s)
    ELAPSED_SEC=$((END_TIME - START_TIME))
    ELAPSED_MIN=$((ELAPSED_SEC / 60))
    ELAPSED_REM_SEC=$((ELAPSED_SEC % 60))
    ELAPSED_DISPLAY="${ELAPSED_MIN}m ${ELAPSED_REM_SEC}s"

    QUOTED_PROMPT=$(echo "$PROMPT" | sed 's/^/> /')

    COMMENT_BODY="## Backlite Task

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
| Elapsed Time | \`${ELAPSED_DISPLAY}\` |
| Max Turns | \`${MAX_TURNS}\` |
| Attempts | \`${ATTEMPT}/${MAX_RETRIES}\` |"
    if [ "$SELF_REVIEW" = "true" ]; then
        COMMENT_BODY="${COMMENT_BODY}
| Self-Review | enabled |"
    fi

    gh pr comment "$PR_URL" --body "$COMMENT_BODY" 2>/dev/null || true
fi

# Self-review used to happen here in the same container. It now runs as a
# chained review task spawned by the orchestrator (internal/orchestrator/chain),
# so this stage is intentionally empty when SELF_REVIEW=true. The "Self-Review"
# label on the PR comment above is the user-facing hint that a follow-up review
# task is on its way.

# --- Write status ---
ELAPSED_SEC=$(( $(date +%s) - START_TIME ))
write_status "$CLAUDE_EXIT" "$COMPLETE" "$NEEDS_INPUT" "$QUESTION" "$ERROR_MSG" "$PR_URL" "$ACTUAL_COST" "$ELAPSED_SEC" "$REPO_URL" "$TARGET_BRANCH" "$TASK_TYPE"

echo "==> Done (exit code: ${CLAUDE_EXIT})"
exit $CLAUDE_EXIT
