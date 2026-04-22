#!/usr/bin/env bash
set -euo pipefail

# --- Configuration from environment ---
# The orchestrator passes the URL in PROMPT (matching the DB column).
# Alias to URL for readability in the reading prompt below.
if [ -z "${PROMPT:-}" ]; then
    echo "ERROR: PROMPT is required (the URL to read)" >&2
    exit 1
fi
URL="$PROMPT"
HARNESS="${HARNESS:-claude_code}"
MODEL="${MODEL:-claude-sonnet-4-6}"
EFFORT="${EFFORT:-medium}"
MAX_BUDGET_USD="${MAX_BUDGET_USD:-5}"
MAX_TURNS="${MAX_TURNS:-50}"
WORKSPACE="${READER_WORKSPACE:-/home/agent/workspace}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/reader_helpers.sh"

# --- Harness auth (validated before any filesystem work) ---
echo "==> Harness: ${HARNESS}"
echo "==> Model: ${MODEL}, effort: ${EFFORT}"
if [ "$HARNESS" = "codex" ]; then
    if [ -z "${OPENAI_API_KEY:-}" ]; then
        echo "ERROR: OPENAI_API_KEY is required for codex harness" >&2
        exit 1
    fi
elif [ "$HARNESS" = "claude_code" ]; then
    if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
        echo "ERROR: ANTHROPIC_API_KEY is required for claude_code harness" >&2
        exit 1
    fi
else
    echo "ERROR: unknown HARNESS '${HARNESS}' (expected claude_code or codex)" >&2
    exit 1
fi

mkdir -p "$WORKSPACE"
STATUS_FILE="${WORKSPACE}/status.json"
export STATUS_FILE
START_TIME=$(date +%s)

# shellcheck disable=SC1091
source "${SCRIPT_DIR}/status_writer.sh"

# --- S3 offloading (optional, matches agent entrypoint behavior) ---
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
URL="$PROMPT"  # re-alias after potential S3 download

if [ "$HARNESS" = "codex" ]; then
    echo "==> Logging in to codex with API key..."
    echo "$OPENAI_API_KEY" | codex login --with-api-key
fi

# --- Backflow API + OpenAI access for helper scripts ---
if [ -z "${BACKFLOW_API_BASE_URL:-}" ]; then
    echo "WARNING: BACKFLOW_API_BASE_URL is not set; read-lookup.sh and read-similar.sh will fail." >&2
fi
if [ -z "${OPENAI_API_KEY:-}" ]; then
    echo "WARNING: OPENAI_API_KEY is not set; read-embed.sh and read-similar.sh will fail." >&2
fi

# =============================================================================
# READING PROMPT
# =============================================================================

READING_PROMPT="You are a reading agent. Your job is to read the URL below, summarize it, and produce a single JSON object describing the reading.

URL: ${URL}

Helper scripts available on PATH (in ${SCRIPT_DIR}):
- ${SCRIPT_DIR}/read-lookup.sh <url>       → exact duplicate check (returns JSON array; empty = no match)
- ${SCRIPT_DIR}/read-embed.sh <text>       → embed text, returns JSON float array
- ${SCRIPT_DIR}/read-similar.sh <text> [n] → find semantically similar existing readings

Required steps:

1. Run ${SCRIPT_DIR}/read-lookup.sh \"${URL}\". If the output is a non-empty array, the URL is a duplicate.
   Use the existing reading's id, title, and tldr and set novelty_verdict to \"duplicate\". Skip fetching the page.

2. Otherwise, fetch the URL's content. With claude_code, use the WebFetch tool. With codex, use curl.

3. Draft: title, tldr (<= 280 chars), tags (lowercase slugs), keywords, people, orgs, summary_markdown.

4. Run echo \"<tldr>\" | ${SCRIPT_DIR}/read-similar.sh to discover semantically similar existing readings.

5. Judge novelty: \"novel\" (no meaningful overlap), \"extends_existing\" (adds to a known topic), or \"duplicate\" (same URL or substantively identical content).

6. Build connections[] from the similar readings you used (each: {reading_id, reason}). Empty array is fine if nothing related.

7. Output a single fenced JSON code block containing ONLY this object (no prose before or after):

\`\`\`json
{
  \"url\": \"...\",
  \"title\": \"...\",
  \"tldr\": \"...\",
  \"tags\": [],
  \"keywords\": [],
  \"people\": [],
  \"orgs\": [],
  \"novelty_verdict\": \"novel|extends_existing|duplicate\",
  \"connections\": [{\"reading_id\": \"...\", \"reason\": \"...\"}],
  \"summary_markdown\": \"...\"
}
\`\`\`

Do not write any other files. Do not make commits or PRs."

# =============================================================================
# RUN HARNESS
# =============================================================================

cd "$WORKSPACE"
HARNESS_LOG="${WORKSPACE}/container_output.log"

echo "==> Running ${HARNESS} for URL: ${URL}"
set +e
if [ "$HARNESS" = "codex" ]; then
    codex exec \
        --model "$MODEL" \
        -c "model_reasoning_effort=${EFFORT}" \
        --dangerously-bypass-approvals-and-sandbox \
        "$READING_PROMPT" 2>&1 | tee "$HARNESS_LOG"
else
    claude -p "$READING_PROMPT" \
        --dangerously-skip-permissions \
        --model "$MODEL" \
        --effort "$EFFORT" \
        --max-turns "$MAX_TURNS" \
        --output-format stream-json \
        --verbose \
        --max-budget-usd "$MAX_BUDGET_USD" 2>&1 | tee "$HARNESS_LOG"
fi
HARNESS_EXIT=${PIPESTATUS[0]}
set -e

# --- Extract the result text from the transcript ---
if [ "$HARNESS" = "codex" ]; then
    RESULT_TEXT=$(cat "$HARNESS_LOG")
    COST_USD="0"
else
    RESULT_LINE=$(grep '"type":"result"' "$HARNESS_LOG" 2>/dev/null | tail -1 || true)
    RESULT_TEXT=""
    COST_USD="0"
    if [ -n "$RESULT_LINE" ]; then
        RESULT_TEXT=$(printf '%s' "$RESULT_LINE" | jq -r '.result // empty' 2>/dev/null || true)
        COST_USD=$(printf '%s' "$RESULT_LINE" | jq -r '.total_cost_usd // 0' 2>/dev/null || echo "0")
    fi
fi

ELAPSED_SEC=$(( $(date +%s) - START_TIME ))

if [ $HARNESS_EXIT -ne 0 ] || [ -z "$RESULT_TEXT" ]; then
    ERROR_MSG="Reader harness failed (exit ${HARNESS_EXIT})"
    if [ -z "$RESULT_TEXT" ]; then
        ERROR_MSG="${ERROR_MSG}; no result text produced"
    fi
    # Harness can exit 0 while still producing no usable result. Force a
    # non-zero exit in that case so the container status reflects the failure.
    EFFECTIVE_EXIT=$HARNESS_EXIT
    if [ "$EFFECTIVE_EXIT" -eq 0 ]; then
        EFFECTIVE_EXIT=1
    fi
    empty_reading=$(jq -n --arg url "$URL" '{
        url: $url, title: "", tldr: "", tags: [], keywords: [], people: [], orgs: [],
        novelty_verdict: "", connections: [], summary_markdown: ""
    }')
    write_reader_status "$EFFECTIVE_EXIT" false false "" "$ERROR_MSG" "" "$COST_USD" "$ELAPSED_SEC" "" "" "read" "$empty_reading" || true
    exit "$EFFECTIVE_EXIT"
fi

# --- Parse the JSON object out of the result text ---
READING_JSON=$(extract_reading_json "$RESULT_TEXT" 2>/dev/null || true)
if [ -z "$READING_JSON" ]; then
    empty_reading=$(jq -n --arg url "$URL" '{
        url: $url, title: "", tldr: "", tags: [], keywords: [], people: [], orgs: [],
        novelty_verdict: "", connections: [], summary_markdown: ""
    }')
    write_reader_status 1 false false "" "Could not extract reading JSON from agent output" "" "$COST_USD" "$ELAPSED_SEC" "" "" "read" "$empty_reading" || true
    exit 1
fi

write_reader_status 0 true false "" "" "" "$COST_USD" "$ELAPSED_SEC" "" "" "read" "$READING_JSON"

echo "==> Done (exit code: 0)"
exit 0
