#!/bin/sh
set -eu

: "${FAKE_OUTCOME:?FAKE_OUTCOME env var is required}"

STATUS_DIR="/home/agent/workspace"
STATUS_FILE="${STATUS_DIR}/status.json"

write_status() {
    exit_code="$1"
    complete="$2"
    needs_input="$3"
    question="$4"
    error_msg="$5"

    mkdir -p "$STATUS_DIR"
    # NOTE: String values are interpolated directly without JSON escaping (no jq
    # in this image). Keep test values free of quotes, backslashes, and newlines.
    # exit_code is included for parity with the real status_writer.sh but is not
    # consumed by orchestrator.AgentStatus — the Go struct ignores unknown keys.
    json="{\"exit_code\":${exit_code},\"complete\":${complete},\"needs_input\":${needs_input},\"question\":\"${question}\",\"error\":\"${error_msg}\",\"pr_url\":\"\",\"cost_usd\":0,\"elapsed_time_sec\":1,\"repo_url\":\"\",\"target_branch\":\"\",\"task_mode\":\"\"}"
    echo "$json" > "$STATUS_FILE"
    echo "BACKFLOW_STATUS_JSON:${json}"
}

echo "FAKE_AGENT: running with outcome=${FAKE_OUTCOME}"

case "$FAKE_OUTCOME" in
    success)
        write_status 0 true false "" ""
        exit 0
        ;;
    slow_success)
        sleep 2
        write_status 0 true false "" ""
        exit 0
        ;;
    fail)
        write_status 1 false false "" "fake agent failure"
        exit 1
        ;;
    needs_input)
        write_status 1 false true "fake question" ""
        exit 1
        ;;
    timeout)
        # Use a signal-aware sleep so Docker stop terminates quickly.
        # PID 1 ignores SIGTERM by default; installing a trap exits cleanly.
        trap 'exit 0' TERM INT
        sleep infinity &
        wait
        ;;
    crash)
        # exit 137 simulates SIGKILL (128+9). Direct kill -9 $$ doesn't work
        # as PID 1 in Docker without --init.
        exit 137
        ;;
    *)
        echo "unknown FAKE_OUTCOME: ${FAKE_OUTCOME}" >&2
        exit 1
        ;;
esac
