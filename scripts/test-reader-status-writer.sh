#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/docker/reader/status_writer.sh"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

STATUS_FILE="${tmpdir}/status.json"

# --- Base write_status still works unchanged ---
base_output="$(write_status 1 false false "" "boom" "" 0.5 7 "" "" "read")"
base_expected="BACKFLOW_STATUS_JSON:$(jq -c . "$STATUS_FILE")"

if ! jq -e '
  .exit_code == 1 and
  .complete == false and
  .error == "boom" and
  .cost_usd == 0.5 and
  .elapsed_time_sec == 7 and
  .task_mode == "read"
' "$STATUS_FILE" >/dev/null; then
  echo "base status.json did not match expected content" >&2
  jq . "$STATUS_FILE" >&2
  exit 1
fi

if [ "$base_output" != "$base_expected" ]; then
  echo "unexpected base BACKFLOW_STATUS_JSON output" >&2
  printf 'got:      %s\n' "$base_output" >&2
  printf 'expected: %s\n' "$base_expected" >&2
  exit 1
fi

# --- write_reader_status merges reading fields flat into status.json ---
reading_json='{
  "url": "https://example.com/article",
  "title": "Example Article",
  "tldr": "Short summary",
  "tags": ["ai", "research"],
  "keywords": ["attention", "transformer"],
  "people": ["Ada Lovelace"],
  "orgs": ["OpenAI"],
  "novelty_verdict": "novel",
  "connections": [{"reading_id": "abc123", "reason": "same topic"}],
  "summary_markdown": "# Heading\n\nBody"
}'

reader_output="$(write_reader_status 0 true false "" "" "" 0.05 45 "" "" "read" "$reading_json")"
reader_expected="BACKFLOW_STATUS_JSON:$(jq -c . "$STATUS_FILE")"

if ! jq -e '
  .exit_code == 0 and
  .complete == true and
  .task_mode == "read" and
  .cost_usd == 0.05 and
  .elapsed_time_sec == 45 and
  .url == "https://example.com/article" and
  .title == "Example Article" and
  .tldr == "Short summary" and
  (.tags | length) == 2 and
  .tags[0] == "ai" and
  (.keywords | length) == 2 and
  (.people | length) == 1 and
  .people[0] == "Ada Lovelace" and
  (.orgs | length) == 1 and
  .novelty_verdict == "novel" and
  (.connections | length) == 1 and
  .connections[0].reading_id == "abc123" and
  .connections[0].reason == "same topic" and
  .summary_markdown == "# Heading\n\nBody"
' "$STATUS_FILE" >/dev/null; then
  echo "reader status.json did not match expected content" >&2
  jq . "$STATUS_FILE" >&2
  exit 1
fi

if [ "$reader_output" != "$reader_expected" ]; then
  echo "unexpected reader BACKFLOW_STATUS_JSON output" >&2
  printf 'got:      %s\n' "$reader_output" >&2
  printf 'expected: %s\n' "$reader_expected" >&2
  exit 1
fi

# --- Invalid reading JSON should surface as an error (non-zero exit) ---
if write_reader_status 0 true false "" "" "" 0 0 "" "" "read" "not-json" 2>/dev/null; then
  echo "write_reader_status should reject invalid reading JSON" >&2
  exit 1
fi

echo "ok"
