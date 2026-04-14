#!/usr/bin/env bash
set -euo pipefail

# read-similar.sh — find readings semantically similar to the given text.
# Usage: read-similar.sh [text] [match_count]
#   text from $1 or stdin; match_count defaults to 5.
# Output: JSON array of {id, title, tldr, url, similarity}.

if [ -z "${OPENAI_API_KEY:-}" ]; then
    echo "read-similar: OPENAI_API_KEY is not set" >&2
    exit 1
fi
if [ -z "${SUPABASE_URL:-}" ]; then
    echo "read-similar: SUPABASE_URL is not set" >&2
    exit 1
fi
if [ -z "${SUPABASE_ANON_KEY:-}" ]; then
    echo "read-similar: SUPABASE_ANON_KEY is not set" >&2
    exit 1
fi

if [ $# -ge 1 ] && [ -n "$1" ]; then
    INPUT="$1"
else
    INPUT="$(cat)"
fi
if [ -z "$INPUT" ]; then
    echo "read-similar: input text is empty" >&2
    exit 1
fi

MATCH_COUNT="${2:-5}"

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EMBEDDING=$(printf '%s' "$INPUT" | "$DIR/read-embed.sh") || {
    echo "read-similar: embedding step failed" >&2
    exit 1
}

REQUEST_BODY=$(jq -n --argjson embedding "$EMBEDDING" --argjson count "$MATCH_COUNT" \
    '{query_embedding: $embedding, match_count: $count}')

RESPONSE=$(curl -fsS \
    -H "apikey: ${SUPABASE_ANON_KEY}" \
    -H "Content-Type: application/json" \
    -H "Content-Profile: reader" \
    -d "$REQUEST_BODY" \
    "${SUPABASE_URL}/rest/v1/rpc/match_readings") || {
    echo "read-similar: Supabase match_readings RPC failed" >&2
    exit 1
}

if ! printf '%s' "$RESPONSE" | jq -e 'type == "array"' >/dev/null 2>&1; then
    echo "read-similar: unexpected response from Supabase: $RESPONSE" >&2
    exit 1
fi

printf '%s\n' "$RESPONSE" | jq -c .
