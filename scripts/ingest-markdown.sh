#!/usr/bin/env bash
set -euo pipefail

# Ingest a local markdown file into the Backlite reading library by POSTing
# its contents to the existing /api/v1/tasks endpoint with task_mode=read and
# the file body in the inline_content field. The server persists the body to
# {DATA_DIR}/ingest/<sha>.md, runs the reader pipeline, and writes a row in
# the readings table.
#
# Usage:
#   ./scripts/ingest-markdown.sh path/to/note.md
#
# Env:
#   BACKFLOW_URL   Base URL of the Backlite server (default http://localhost:8080)

BACKFLOW_URL="${BACKFLOW_URL:-http://localhost:8080}"

usage() {
    cat <<USAGE
Usage: $(basename "$0") <path/to/file.md>

Reads the markdown file at <path>, POSTs it to ${BACKFLOW_URL}/api/v1/tasks
as a read-mode task with the body in inline_content, and prints the task ID.

The file must exist, be a regular file, be readable, and be non-empty.
USAGE
    exit 1
}

if [ $# -lt 1 ]; then
    usage
fi

FILE="$1"

if [ ! -e "$FILE" ]; then
    echo "Error: file not found: $FILE" >&2
    exit 1
fi
if [ -d "$FILE" ]; then
    echo "Error: $FILE is a directory; expected a regular file" >&2
    exit 1
fi
if [ ! -f "$FILE" ]; then
    echo "Error: $FILE is not a regular file" >&2
    exit 1
fi
if [ ! -r "$FILE" ]; then
    echo "Error: $FILE is not readable" >&2
    exit 1
fi
if [ ! -s "$FILE" ]; then
    echo "Error: $FILE is empty" >&2
    exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
    echo "Error: jq is required" >&2
    exit 1
fi

# Build the JSON body. jq -Rs '.' reads stdin verbatim and produces a JSON
# string (handles all escaping for us — newlines, quotes, backslashes).
JSON=$(jq -Rs --arg mode "read" --arg prompt "ingest local markdown file: $(basename "$FILE")" '{
    task_mode: $mode,
    prompt: $prompt,
    inline_content: .
}' < "$FILE")

RESPONSE=$(curl -s -w "\n%{http_code}" \
    -X POST "${BACKFLOW_URL}/api/v1/tasks" \
    -H "Content-Type: application/json" \
    -d "$JSON")

HTTP_CODE=$(printf '%s\n' "$RESPONSE" | tail -1)
BODY=$(printf '%s\n' "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" = "201" ]; then
    TASK_ID=$(printf '%s' "$BODY" | jq -r '.data.id')
    echo "$TASK_ID"
else
    echo "Error (HTTP $HTTP_CODE):" >&2
    printf '%s\n' "$BODY" | jq . 2>/dev/null || printf '%s\n' "$BODY" >&2
    exit 1
fi
