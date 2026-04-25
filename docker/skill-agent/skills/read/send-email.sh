#!/usr/bin/env bash
set -euo pipefail

# send-email.sh — send a plain-text email summary of a read-mode reading
# via Resend.
#
# Usage: send-email.sh [status.json path]
#   status.json path defaults to $HOME/workspace/status.json.
#
# No-op (exit 0 with a log line) when RESEND_API_KEY, NOTIFY_EMAIL_FROM, or
# NOTIFY_EMAIL_TO is unset, or when the status.json file is missing. Email
# delivery failures are logged and exit 0 as well — email is advisory and
# must not block task completion.

STATUS_JSON="${1:-${HOME}/workspace/status.json}"

if [ -z "${RESEND_API_KEY:-}" ]; then
    echo "send-email: RESEND_API_KEY not set; skipping email send" >&2
    exit 0
fi
if [ -z "${NOTIFY_EMAIL_FROM:-}" ] || [ -z "${NOTIFY_EMAIL_TO:-}" ]; then
    echo "send-email: NOTIFY_EMAIL_FROM or NOTIFY_EMAIL_TO not set; skipping email send" >&2
    exit 0
fi
if [ ! -f "$STATUS_JSON" ]; then
    echo "send-email: status file $STATUS_JSON not found; skipping email send" >&2
    exit 0
fi

URL=$(jq -r '.url // ""' "$STATUS_JSON")
TITLE=$(jq -r '.title // ""' "$STATUS_JSON")
TLDR=$(jq -r '.tldr // ""' "$STATUS_JSON")
TASK_ID_VAL="${TASK_ID:-}"

BODY=$(printf 'URL: %s\nTitle: %s\nTL;DR: %s\n\nTask: %s\n' \
    "$URL" "$TITLE" "$TLDR" "$TASK_ID_VAL")

PAYLOAD=$(jq -n \
    --arg from "$NOTIFY_EMAIL_FROM" \
    --arg to "$NOTIFY_EMAIL_TO" \
    --arg subject "$TITLE" \
    --arg text "$BODY" \
    '{from: $from, to: [$to], subject: $subject, text: $text}')

BASE_URL="${RESEND_BASE_URL:-https://api.resend.com}"

if ! curl -fsS \
    -H "Authorization: Bearer ${RESEND_API_KEY}" \
    -H "Content-Type: application/json" \
    -d "$PAYLOAD" \
    "${BASE_URL}/emails" >/dev/null; then
    echo "send-email: Resend API request failed; continuing" >&2
    exit 0
fi

echo "send-email: sent email summary for task ${TASK_ID_VAL}" >&2
