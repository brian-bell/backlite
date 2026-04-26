#!/usr/bin/env bash
set -euo pipefail

# fetch-and-extract.sh — pre-fetches the URL and (for HTML) writes a
# Readability-derived markdown extraction. Outputs in $WORKSPACE:
#   raw.html       Raw bytes from the URL.
#   extracted.md   Markdown extraction (HTML only).
#   content.json   Sidecar metadata (mirrored to the readings DB row).
#
# This step is non-fatal for the reader pipeline: failures here log a
# warning and the agent still runs with WebFetch as the read path. Slice 1
# of the content-capture feature only ships the HTML happy path; richer
# failure-status semantics land in a follow-up slice.

if [ -z "${URL:-}" ]; then
    echo "ERROR: URL is required" >&2
    exit 1
fi
WORKSPACE="${WORKSPACE:-/home/agent/workspace}"
mkdir -p "$WORKSPACE"

EXTRACT_CMD="${EXTRACT_CMD:-node /home/agent/extractor/extract.js}"

RAW_PATH="$WORKSPACE/raw.html"
EXTRACTED_PATH="$WORKSPACE/extracted.md"
SIDECAR_PATH="$WORKSPACE/content.json"
HEADERS_PATH="$WORKSPACE/.headers.txt"

# --- Fetch ---
# -L: follow redirects. -sS: silent but show errors. -A: identify ourselves.
# We deliberately do not pass --max-filesize here — slice 2 wires that in.
if ! curl -L -sS -A "BacklliteReader/1.0" \
        -D "$HEADERS_PATH" \
        -o "$RAW_PATH" \
        "$URL"; then
    echo "WARN: curl failed for $URL; skipping content capture" >&2
    rm -f "$RAW_PATH" "$HEADERS_PATH"
    exit 0
fi

# --- Parse content type from the response headers ---
# Strip CR, take the last Content-Type (final response after redirects).
CONTENT_TYPE=$(awk -F': ' 'tolower($1)=="content-type"{val=$2} END{print val}' "$HEADERS_PATH" | tr -d '\r' | sed 's/[[:space:]]*$//')
if [ -z "$CONTENT_TYPE" ]; then
    CONTENT_TYPE="application/octet-stream"
fi

CONTENT_BYTES=$(wc -c < "$RAW_PATH" | tr -d ' ')

# --- Extract markdown for HTML ---
EXTRACTED_BYTES=0
if echo "$CONTENT_TYPE" | grep -qi 'text/html'; then
    if $EXTRACT_CMD "$RAW_PATH" "$EXTRACTED_PATH" 2>/dev/null; then
        if [ -f "$EXTRACTED_PATH" ]; then
            EXTRACTED_BYTES=$(wc -c < "$EXTRACTED_PATH" | tr -d ' ')
        fi
    else
        echo "WARN: extractor failed for $URL; persisting raw only" >&2
        rm -f "$EXTRACTED_PATH"
    fi
fi

# --- SHA-256 of raw bytes ---
if command -v sha256sum >/dev/null 2>&1; then
    SHA256=$(sha256sum "$RAW_PATH" | awk '{print $1}')
else
    # Fallback for environments without coreutils sha256sum (e.g. macOS).
    SHA256=$(shasum -a 256 "$RAW_PATH" | awk '{print $1}')
fi

FETCHED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# --- Write sidecar JSON ---
jq -n \
    --arg url "$URL" \
    --arg ct "$CONTENT_TYPE" \
    --arg status "captured" \
    --argjson cb "$CONTENT_BYTES" \
    --argjson eb "$EXTRACTED_BYTES" \
    --arg sha "$SHA256" \
    --arg fetched "$FETCHED_AT" '{
        url: $url,
        content_type: $ct,
        content_status: $status,
        content_bytes: $cb,
        extracted_bytes: $eb,
        content_sha256: $sha,
        fetched_at: $fetched
    }' > "$SIDECAR_PATH"

rm -f "$HEADERS_PATH"
exit 0
