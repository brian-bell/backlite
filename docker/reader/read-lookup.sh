#!/usr/bin/env bash
set -euo pipefail

# read-lookup.sh — look up an existing reading by exact URL match.
# Usage: read-lookup.sh <url>
# Output: JSON array (empty = no match, single element = duplicate).

if [ -z "${BACKFLOW_API_BASE_URL:-}" ]; then
    echo "read-lookup: BACKFLOW_API_BASE_URL is not set" >&2
    exit 1
fi

if [ $# -lt 1 ] || [ -z "$1" ]; then
    echo "read-lookup: URL argument is required" >&2
    exit 1
fi

URL="$1"
ENCODED=$(printf '%s' "$URL" | jq -sRr @uri)

AUTH_ARGS=()
if [ -n "${BACKFLOW_API_KEY:-}" ]; then
    AUTH_ARGS=(-H "Authorization: Bearer ${BACKFLOW_API_KEY}")
fi

RESPONSE=$(curl -fsS \
    "${AUTH_ARGS[@]}" \
    "${BACKFLOW_API_BASE_URL}/api/v1/readings/lookup?url=${ENCODED}") || {
    echo "read-lookup: Backlite API request failed" >&2
    exit 1
}

if ! printf '%s' "$RESPONSE" | jq -e '.data | type == "array"' >/dev/null 2>&1; then
    echo "read-lookup: unexpected response from Backlite API: $RESPONSE" >&2
    exit 1
fi

printf '%s\n' "$RESPONSE" | jq -c '.data'
