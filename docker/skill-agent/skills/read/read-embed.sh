#!/usr/bin/env bash
set -euo pipefail

# read-embed.sh — embed text via OpenAI text-embedding-3-small.
# Input: text from argument $1 or stdin. Output: JSON float array on stdout.

if [ -z "${OPENAI_API_KEY:-}" ]; then
    echo "read-embed: OPENAI_API_KEY is not set" >&2
    exit 1
fi

if [ $# -ge 1 ]; then
    INPUT="$1"
else
    INPUT="$(cat)"
fi

if [ -z "$INPUT" ]; then
    echo "read-embed: input text is empty" >&2
    exit 1
fi

MODEL="${EMBEDDING_MODEL:-text-embedding-3-small}"

REQUEST_BODY=$(jq -n --arg input "$INPUT" --arg model "$MODEL" \
    '{input: $input, model: $model}')

RESPONSE=$(curl -fsS \
    -H "Authorization: Bearer ${OPENAI_API_KEY}" \
    -H "Content-Type: application/json" \
    -d "$REQUEST_BODY" \
    https://api.openai.com/v1/embeddings) || {
    echo "read-embed: OpenAI embeddings API request failed" >&2
    exit 1
}

EMBEDDING=$(printf '%s' "$RESPONSE" | jq -c '.data[0].embedding // empty')
if [ -z "$EMBEDDING" ] || [ "$EMBEDDING" = "null" ]; then
    echo "read-embed: unexpected response from OpenAI: $RESPONSE" >&2
    exit 1
fi

printf '%s\n' "$EMBEDDING"
