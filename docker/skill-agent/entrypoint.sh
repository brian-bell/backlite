#!/usr/bin/env bash
# Skill-agent entrypoint: thin runner that validates env, fetches S3-offloaded
# fields, sets up auth, installs the requested skill bundle, runs the harness,
# and notarizes cost_usd into the agent-written status.json (or synthesizes a
# fallback failure status if status.json is missing).
#
# This entrypoint deliberately has no per-mode or per-harness branching: only
# claude_code is supported, and modes are expressed as skill bundles, not
# shell branches.
set -euo pipefail

# --- Required env ---
: "${PROMPT:?PROMPT is required}"
: "${TASK_ID:?TASK_ID is required}"
TASK_MODE="${TASK_MODE:-code}"
HARNESS="${HARNESS:-claude_code}"
MODEL="${MODEL:-claude-sonnet-4-6}"
EFFORT="${EFFORT:-medium}"
MAX_BUDGET_USD="${MAX_BUDGET_USD:-10}"
MAX_TURNS="${MAX_TURNS:-200}"

if [ "$HARNESS" != "claude_code" ]; then
    echo "ERROR: skill-agent image only supports the claude_code harness; got '${HARNESS}'" >&2
    exit 2
fi
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
    echo "ERROR: ANTHROPIC_API_KEY is required for claude_code harness" >&2
    exit 2
fi

# --- S3 offload (optional, matches the old images' behavior) ---
fetch_s3_var() {
    local url_var="$1"
    local target_var="$2"
    local url="${!url_var:-}"
    if [ -n "$url" ]; then
        echo "==> Downloading ${target_var} from S3..."
        local content
        content=$(curl -fsSL "$url") || { echo "ERROR: failed to download ${target_var}" >&2; exit 1; }
        printf -v "$target_var" '%s' "$content"
        export "$target_var"
    fi
}
fetch_s3_var "PROMPT_S3_URL" "PROMPT"
fetch_s3_var "CLAUDE_MD_S3_URL" "CLAUDE_MD"
fetch_s3_var "TASK_CONTEXT_S3_URL" "TASK_CONTEXT"
fetch_s3_var "PR_BODY_S3_URL" "PR_BODY"

# --- GitHub auth ---
if [ -n "${GITHUB_TOKEN:-}" ]; then
    echo "$GITHUB_TOKEN" | gh auth login --with-token 2>/dev/null || true
    gh auth setup-git 2>/dev/null || true
fi

# --- Install the requested skill bundle ---
# SKILLS_SRC defaults to the bake-in path used inside the docker image. Tests
# may override this to point at the source tree.
SKILLS_SRC="${BACKFLOW_SKILLS_DIR:-/opt/backflow/skills}"
SKILL_DIR="${SKILLS_SRC}/${TASK_MODE}"
if [ ! -d "$SKILL_DIR" ]; then
    echo "ERROR: no skill bundle for task_mode='${TASK_MODE}' at ${SKILL_DIR}" >&2
    exit 2
fi
mkdir -p "${HOME}/.claude/skills"
cp -r "$SKILL_DIR" "${HOME}/.claude/skills/${TASK_MODE}"

# Auto mode dispatches to code or review at runtime, so the auto skill needs
# both bundles installed alongside it. Skip any sub-bundle that's already in
# place (e.g. when TASK_MODE itself is code/review) to keep the copy idempotent.
if [ "$TASK_MODE" = "auto" ]; then
    for sub in code review; do
        sub_src="${SKILLS_SRC}/${sub}"
        sub_dst="${HOME}/.claude/skills/${sub}"
        if [ -d "$sub_src" ] && [ ! -d "$sub_dst" ]; then
            cp -r "$sub_src" "$sub_dst"
        fi
    done
fi

WORKSPACE="${HOME}/workspace"
mkdir -p "$WORKSPACE"
STATUS_FILE="${WORKSPACE}/status.json"
START_TIME=$(date +%s)

# Starter prompt: brief enough to leave room for the skill to drive the run.
STARTER_PROMPT="Use the '${TASK_MODE}' skill from ~/.claude/skills/${TASK_MODE}/SKILL.md to handle the following task.

Task:
${PROMPT}"

# --- Run the harness, capturing stream-json so we can notarize cost_usd ---
HARNESS_LOG="/tmp/container_output.log"
echo "==> Running claude_code with skill='${TASK_MODE}'"

set +e
claude -p "$STARTER_PROMPT" \
    --dangerously-skip-permissions \
    --model "$MODEL" \
    --effort "$EFFORT" \
    --max-turns "$MAX_TURNS" \
    --output-format stream-json \
    --verbose \
    --max-budget-usd "$MAX_BUDGET_USD" 2>&1 | tee "$HARNESS_LOG"
HARNESS_EXIT=${PIPESTATUS[0]}
set -e

ELAPSED_SEC=$(( $(date +%s) - START_TIME ))

# --- Cost notarization: pull total_cost_usd from the harness stream-json ---
# Last "type":"result" line carries the running total.
COST_USD="0"
RESULT_LINE=$(grep '"type":"result"' "$HARNESS_LOG" 2>/dev/null | tail -1 || true)
if [ -n "$RESULT_LINE" ]; then
    COST_USD=$(printf '%s' "$RESULT_LINE" | jq -r '.total_cost_usd // 0' 2>/dev/null || echo "0")
fi

# --- Status notarization: merge cost_usd into agent's status.json or
# synthesize a fallback failure status if missing/unparsable.
if [ -f "$STATUS_FILE" ] && jq -e . "$STATUS_FILE" >/dev/null 2>&1; then
    tmp=$(mktemp)
    jq --argjson cost "$COST_USD" --arg elapsed "$ELAPSED_SEC" \
        '.cost_usd = $cost | .elapsed_time_sec = ($elapsed | tonumber)' \
        "$STATUS_FILE" > "$tmp"
    mv "$tmp" "$STATUS_FILE"
    echo "==> Notarized cost_usd=${COST_USD} into status.json"
else
    echo "==> No usable status.json from agent; synthesizing fallback failure status" >&2
    jq -n \
        --argjson exit "$HARNESS_EXIT" \
        --argjson cost "$COST_USD" \
        --arg elapsed "$ELAPSED_SEC" \
        --arg mode "$TASK_MODE" \
        --arg err "agent did not write a parseable status.json (harness exit ${HARNESS_EXIT})" \
        '{
            exit_code: $exit,
            complete: false,
            needs_input: false,
            error: $err,
            cost_usd: $cost,
            elapsed_time_sec: ($elapsed | tonumber),
            task_mode: $mode
        }' > "$STATUS_FILE"
fi

# Exit code mirrors the harness so the orchestrator's docker inspect still sees
# the failure signal, but successful agent runs win even when the harness
# itself returned non-zero (the agent owns the success determination).
if [ "$(jq -r '.complete // false' "$STATUS_FILE")" = "true" ]; then
    exit 0
fi
exit "$HARNESS_EXIT"
