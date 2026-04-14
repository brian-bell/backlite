#!/usr/bin/env bash

# Shared helper for writing the reader agent status file and the Fargate log payload.

json_string() {
    printf '%s' "$1" | jq -Rs .
}

write_status() {
    local exit_code="$1"
    local complete="$2"
    local needs_input="$3"
    local question="$4"
    local error_msg="$5"
    local pr_url="${6:-}"
    local cost_usd="${7:-0}"
    local elapsed_sec="${8:-0}"
    local repo_url="${9:-}"
    local target_branch="${10:-}"
    local task_type="${11:-}"

    : "${STATUS_FILE:?STATUS_FILE is required}"

    cat > "$STATUS_FILE" <<STATUSEOF
{
  "exit_code": ${exit_code},
  "complete": ${complete},
  "needs_input": ${needs_input},
  "question": $(json_string "$question"),
  "error": $(json_string "$error_msg"),
  "pr_url": $(json_string "$pr_url"),
  "cost_usd": ${cost_usd},
  "elapsed_time_sec": ${elapsed_sec},
  "repo_url": $(json_string "$repo_url"),
  "target_branch": $(json_string "$target_branch"),
  "task_mode": $(json_string "$task_type")
}
STATUSEOF

    echo "BACKFLOW_STATUS_JSON:$(jq -c . "$STATUS_FILE")"
}

# write_reader_status extends write_status with a "reading" sub-object.
# Args 1-11 match write_status; arg 12 is a JSON object with reading fields.
write_reader_status() {
    local exit_code="$1"
    local complete="$2"
    local needs_input="$3"
    local question="$4"
    local error_msg="$5"
    local pr_url="${6:-}"
    local cost_usd="${7:-0}"
    local elapsed_sec="${8:-0}"
    local repo_url="${9:-}"
    local target_branch="${10:-}"
    local task_type="${11:-read}"
    local reading_json="${12:-{\}}"

    : "${STATUS_FILE:?STATUS_FILE is required}"

    # Validate reading_json is a JSON object before writing anything.
    if ! printf '%s' "$reading_json" | jq -e 'type == "object"' >/dev/null 2>&1; then
        echo "write_reader_status: reading payload is not a JSON object" >&2
        return 1
    fi

    write_status "$exit_code" "$complete" "$needs_input" "$question" "$error_msg" \
        "$pr_url" "$cost_usd" "$elapsed_sec" "$repo_url" "$target_branch" "$task_type" \
        >/dev/null

    local tmp="${STATUS_FILE}.tmp"
    jq --argjson reading "$reading_json" '. + {reading: $reading}' "$STATUS_FILE" > "$tmp"
    mv "$tmp" "$STATUS_FILE"

    echo "BACKFLOW_STATUS_JSON:$(jq -c . "$STATUS_FILE")"
}
