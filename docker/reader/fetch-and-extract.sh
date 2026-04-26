#!/usr/bin/env bash
set -euo pipefail

# fetch-and-extract.sh — pre-fetches the URL and (for HTML) writes a
# Readability-derived markdown extraction. Outputs in $WORKSPACE:
#   raw.<ext>      Raw bytes from the URL, extension derived from Content-Type.
#   extracted.md   Markdown extraction (HTML only).
#   content.json   Sidecar metadata (mirrored to the readings DB row).
#
# This step is non-fatal for the reader pipeline: failures here log a
# warning and the agent still runs with WebFetch as the read path. Slice 1
# of the content-capture feature only ships the HTML happy path; richer
# failure-status semantics land in a follow-up slice.

WORKSPACE="${WORKSPACE:-/home/agent/workspace}"
mkdir -p "$WORKSPACE"

EXTRACT_CMD="${EXTRACT_CMD:-node /home/agent/extractor/extract.js}"

# Initial download path. We don't yet know the content type, so write to a
# generic filename and rename to raw.<ext> after parsing the response headers.
RAW_DOWNLOAD_PATH="$WORKSPACE/raw.download"
RAW_PATH="$RAW_DOWNLOAD_PATH"
EXTRACTED_PATH="$WORKSPACE/extracted.md"
SIDECAR_PATH="$WORKSPACE/content.json"
HEADERS_PATH="$WORKSPACE/.headers.txt"

# extension_for_content_type maps a Content-Type to a stable file extension.
# Keep in sync with internal/api/handlers.go:rawFilenameForContentType.
extension_for_content_type() {
    local ct="$1"
    case "$(printf '%s' "$ct" | tr '[:upper:]' '[:lower:]')" in
        *text/html*)        printf '%s' "html" ;;
        *application/pdf*)  printf '%s' "pdf"  ;;
        *application/json*) printf '%s' "json" ;;
        *text/plain*)       printf '%s' "txt"  ;;
        *)                  printf '%s' "bin"  ;;
    esac
}

# write_failure_sidecar emits a content.json with the given status and exits 0.
# Used for fetch_failed / over_size_cap; capture is non-fatal so the agent's
# WebFetch path still runs after this script returns.
write_failure_sidecar() {
    local status="$1"
    local fetched_at
    fetched_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    rm -f "$RAW_DOWNLOAD_PATH" "$EXTRACTED_PATH" "$HEADERS_PATH"
    rm -f "$WORKSPACE"/raw.html "$WORKSPACE"/raw.pdf "$WORKSPACE"/raw.json "$WORKSPACE"/raw.txt "$WORKSPACE"/raw.bin
    jq -n \
        --arg url "$URL" \
        --arg status "$status" \
        --arg fetched "$fetched_at" '{
            url: $url,
            content_type: "",
            content_status: $status,
            content_bytes: 0,
            extracted_bytes: 0,
            content_sha256: "",
            fetched_at: $fetched
        }' > "$SIDECAR_PATH"
    exit 0
}

# --- Inline-content short-circuit ---
# When the orchestrator bind-mounts a markdown file into the container and
# sets INLINE_CONTENT_PATH, the URL fetch + extraction pipeline is skipped:
# the file is the extracted markdown.
if [ -n "${INLINE_CONTENT_PATH:-}" ]; then
    if [ ! -f "$INLINE_CONTENT_PATH" ]; then
        echo "ERROR: INLINE_CONTENT_PATH=$INLINE_CONTENT_PATH not found" >&2
        exit 1
    fi
    cp "$INLINE_CONTENT_PATH" "$EXTRACTED_PATH"

    INLINE_BYTES=$(wc -c < "$EXTRACTED_PATH" | tr -d ' ')
    if command -v sha256sum >/dev/null 2>&1; then
        INLINE_SHA=$(sha256sum "$EXTRACTED_PATH" | awk '{print $1}')
    else
        INLINE_SHA=$(shasum -a 256 "$EXTRACTED_PATH" | awk '{print $1}')
    fi
    INLINE_FETCHED_AT=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    jq -n \
        --arg ct "text/markdown" \
        --arg status "captured" \
        --argjson cb "$INLINE_BYTES" \
        --argjson eb "$INLINE_BYTES" \
        --arg sha "$INLINE_SHA" \
        --arg fetched "$INLINE_FETCHED_AT" '{
            content_type: $ct,
            content_status: $status,
            content_bytes: $cb,
            extracted_bytes: $eb,
            content_sha256: $sha,
            fetched_at: $fetched
        }' > "$SIDECAR_PATH"
    exit 0
fi

if [ -z "${URL:-}" ]; then
    echo "ERROR: URL is required" >&2
    exit 1
fi

# --- Fetch ---
# -L: follow redirects. -sS: silent but show errors. -A: identify ourselves.
# -w '%{http_code}' captures the final-response status so we can record
# fetch_failed for 4xx/5xx without parsing headers.
# --max-filesize bounds the download; curl exits 63 when the cap trips.
MAX_FILESIZE_FLAG=""
if [ -n "${MAX_CONTENT_BYTES:-}" ] && [ "$MAX_CONTENT_BYTES" -gt 0 ]; then
    MAX_FILESIZE_FLAG="--max-filesize $MAX_CONTENT_BYTES"
fi

set +e
# shellcheck disable=SC2086  # MAX_FILESIZE_FLAG is intentionally word-split
HTTP_CODE=$(curl -L -sS -A "BackliteReader/1.0" \
        $MAX_FILESIZE_FLAG \
        -D "$HEADERS_PATH" \
        -o "$RAW_PATH" \
        -w '%{http_code}' \
        "$URL" 2>/dev/null)
CURL_EXIT=$?
set -e

if [ "$CURL_EXIT" -eq 63 ]; then
    echo "WARN: $URL exceeded MAX_CONTENT_BYTES=$MAX_CONTENT_BYTES; recording over_size_cap" >&2
    write_failure_sidecar "over_size_cap"
fi
if [ "$CURL_EXIT" -ne 0 ] || [ -z "$HTTP_CODE" ] || [ "$HTTP_CODE" = "000" ]; then
    echo "WARN: curl failed for $URL (exit=$CURL_EXIT); recording fetch_failed" >&2
    write_failure_sidecar "fetch_failed"
fi
if [ "${HTTP_CODE:0:1}" != "2" ]; then
    echo "WARN: HTTP $HTTP_CODE from $URL; recording fetch_failed" >&2
    write_failure_sidecar "fetch_failed"
fi
if [ ! -s "$RAW_PATH" ]; then
    echo "WARN: empty body from $URL; recording fetch_failed" >&2
    write_failure_sidecar "fetch_failed"
fi

# --- Parse content type from the response headers ---
# Strip CR, take the last Content-Type (final response after redirects).
CONTENT_TYPE=$(awk -F': ' 'tolower($1)=="content-type"{val=$2} END{print val}' "$HEADERS_PATH" | tr -d '\r' | sed 's/[[:space:]]*$//')
if [ -z "$CONTENT_TYPE" ]; then
    CONTENT_TYPE="application/octet-stream"
fi

# --- Rename raw.<ext> based on content type ---
EXT=$(extension_for_content_type "$CONTENT_TYPE")
NEW_RAW_PATH="$WORKSPACE/raw.$EXT"
if [ "$RAW_PATH" != "$NEW_RAW_PATH" ]; then
    mv "$RAW_PATH" "$NEW_RAW_PATH"
    RAW_PATH="$NEW_RAW_PATH"
fi

CONTENT_BYTES=$(wc -c < "$RAW_PATH" | tr -d ' ')

# --- Extract markdown for HTML only ---
EXTRACTED_BYTES=0
if [ "$EXT" = "html" ]; then
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
