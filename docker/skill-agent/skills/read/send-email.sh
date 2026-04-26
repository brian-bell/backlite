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
NOVELTY=$(jq -r '.novelty_verdict // ""' "$STATUS_JSON")
TAGS=$(jq -r '(.tags // []) | join(", ")' "$STATUS_JSON")
KEYWORDS=$(jq -r '(.keywords // []) | join(", ")' "$STATUS_JSON")
PEOPLE=$(jq -r '(.people // []) | join(", ")' "$STATUS_JSON")
ORGS=$(jq -r '(.orgs // []) | join(", ")' "$STATUS_JSON")
SUMMARY_MD=$(jq -r '.summary_markdown // ""' "$STATUS_JSON")
CONNECTIONS=$(jq -r '.connections[]? | "- \(.reading_id): \(.reason)"' "$STATUS_JSON")
TASK_ID_VAL="${TASK_ID:-}"

# Subject: page title; fallback to URL hostname when title is empty.
host="${URL#*://}"; host="${host%%/*}"
SUBJECT="${TITLE:-$host}"

BODY="URL: ${URL}"$'\n'
BODY+="Title: ${TITLE}"$'\n'
[ -n "$NOVELTY" ]  && BODY+="Novelty: ${NOVELTY}"$'\n'
[ -n "$TAGS" ]     && BODY+="Tags: ${TAGS}"$'\n'
[ -n "$KEYWORDS" ] && BODY+="Keywords: ${KEYWORDS}"$'\n'
[ -n "$PEOPLE" ]   && BODY+="People: ${PEOPLE}"$'\n'
[ -n "$ORGS" ]     && BODY+="Orgs: ${ORGS}"$'\n'
BODY+=$'\n'"TL;DR: ${TLDR}"$'\n'
if [ -n "$SUMMARY_MD" ]; then
    BODY+=$'\n'"Summary:"$'\n'"${SUMMARY_MD}"$'\n'
fi
if [ -n "$CONNECTIONS" ]; then
    BODY+=$'\n'"Connections:"$'\n'"${CONNECTIONS}"$'\n'
fi
BODY+=$'\n'"Task: ${TASK_ID_VAL}"$'\n'

PAYLOAD=$(jq -n \
    --arg from "$NOTIFY_EMAIL_FROM" \
    --arg to "$NOTIFY_EMAIL_TO" \
    --arg subject "$SUBJECT" \
    --arg text "$BODY" \
    '{from: $from, to: [$to], subject: $subject, text: $text}')

BASE_URL="${RESEND_BASE_URL:-https://api.resend.com}"
IDEMPOTENCY_KEY="${TASK_ID_VAL}:read.completed"
BASE_DELAY="${RESEND_RETRY_BASE_DELAY_SEC:-2}"
MAX_ATTEMPTS=3

attempt=1
while [ "$attempt" -le "$MAX_ATTEMPTS" ]; do
    if [ "$attempt" -gt 1 ]; then
        sleep "$(awk -v a="$attempt" -v d="$BASE_DELAY" 'BEGIN { print (a - 1) * d }')"
    fi

    http_code=$(curl -sS -o /dev/null -w "%{http_code}" \
        -H "Authorization: Bearer ${RESEND_API_KEY}" \
        -H "Content-Type: application/json" \
        -H "Idempotency-Key: ${IDEMPOTENCY_KEY}" \
        -d "$PAYLOAD" \
        "${BASE_URL}/emails" 2>/dev/null || true)

    case "$http_code" in
        2*)
            echo "send-email: sent email summary for task ${TASK_ID_VAL} (attempt $attempt)" >&2
            exit 0
            ;;
        4*)
            echo "send-email: Resend API returned ${http_code}; not retrying" >&2
            exit 0
            ;;
        *)
            echo "send-email: Resend API attempt ${attempt} failed (status ${http_code:-network})" >&2
            ;;
    esac

    attempt=$((attempt + 1))
done

echo "send-email: gave up after ${MAX_ATTEMPTS} attempts" >&2
