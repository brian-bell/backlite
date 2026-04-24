#!/usr/bin/env bash
set -euo pipefail

# End-to-end smoke test for the reader image. Hits real OpenAI + Anthropic APIs
# plus a running Backlite server, so it reads credentials from the repo's .env file.
# Required keys in .env:
#   OPENAI_API_KEY, ANTHROPIC_API_KEY, BACKFLOW_INTERNAL_API_BASE_URL

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ROOT_DIR}/.env"

if [ ! -f "$ENV_FILE" ]; then
    echo "test_reader_e2e: ${ENV_FILE} not found" >&2
    exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

for var in OPENAI_API_KEY ANTHROPIC_API_KEY BACKFLOW_INTERNAL_API_BASE_URL; do
    if [ -z "${!var:-}" ]; then
        echo "test_reader_e2e: ${var} is not set in ${ENV_FILE}" >&2
        exit 1
    fi
done

# Embedding (real OpenAI call)
docker run --rm -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  --entrypoint /home/agent/read-embed.sh backlite-reader \
  "hello world" | jq 'length'   # ~1536

# Exact-URL lookup (real Backlite API call)
docker run --rm \
  -e BACKFLOW_API_BASE_URL="$BACKFLOW_INTERNAL_API_BASE_URL" \
  --entrypoint /home/agent/read-lookup.sh backlite-reader \
  "https://example.com/non-existent" | jq .   # []

# Similarity search (real OpenAI + Backlite API calls)
docker run --rm \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -e BACKFLOW_API_BASE_URL="$BACKFLOW_INTERNAL_API_BASE_URL" \
  --entrypoint /home/agent/read-similar.sh backlite-reader \
  "attention mechanisms in transformers" 3 | jq .

# Full entrypoint run (real ANTHROPIC_API_KEY)
docker run --rm \
  -e PROMPT="https://example.com/article" \
  -e ANTHROPIC_API_KEY="$ANTHROPIC_API_KEY" \
  -e OPENAI_API_KEY="$OPENAI_API_KEY" \
  -e BACKFLOW_API_BASE_URL="$BACKFLOW_INTERNAL_API_BASE_URL" \
  backlite-reader 2>&1 | tail -20
# Expect: a single `BACKFLOW_STATUS_JSON:{...}` line with
# `.task_mode == "read"` and a populated `.reading` object.
