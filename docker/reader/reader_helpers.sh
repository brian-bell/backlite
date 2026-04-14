#!/usr/bin/env bash

# extract_reading_json extracts the first JSON object from a transcript.
# It prefers a fenced ```json ... ``` (or ``` ... ```) block, falling back
# to the first balanced-brace object. Writes the compact JSON to stdout.
extract_reading_json() {
    local text="$1"

    # 1. Prefer the contents of the first fenced block.
    local fenced
    fenced=$(printf '%s\n' "$text" | awk '
        /^[[:space:]]*```/ {
            if (in_block) { in_block=0; exit }
            in_block=1
            next
        }
        in_block { print }
    ')

    if [ -n "$fenced" ] && printf '%s' "$fenced" | jq -e 'type == "object"' >/dev/null 2>&1; then
        printf '%s' "$fenced" | jq -c .
        return 0
    fi

    # 2. Fall back to the first balanced-brace object in the raw text.
    local candidate
    candidate=$(printf '%s\n' "$text" | awk '
        {
            for (i=1; i<=length($0); i++) {
                c = substr($0, i, 1)
                if (!started) {
                    if (c == "{") { started=1; depth=1; buf=c }
                    continue
                }
                buf = buf c
                if (c == "{") depth++
                else if (c == "}") {
                    depth--
                    if (depth == 0) { print buf; exit }
                }
            }
            if (started) buf = buf "\n"
        }')

    if [ -n "$candidate" ] && printf '%s' "$candidate" | jq -e 'type == "object"' >/dev/null 2>&1; then
        printf '%s' "$candidate" | jq -c .
        return 0
    fi

    echo "extract_reading_json: no JSON object found in transcript" >&2
    return 1
}
