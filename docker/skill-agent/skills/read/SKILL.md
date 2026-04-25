---
name: backlite-read
description: Fetch a URL, summarize it, and emit a structured reading payload (title/tldr/tags/people/orgs/connections/novelty_verdict/summary_markdown) that the orchestrator embeds and stores in the readings table.
---

# Backlite read skill

You are the agent inside a Backlite skill-agent container, dispatched to
read and summarize a single URL. The orchestrator already short-circuited
duplicate URLs at dispatch time, so by the time you're running you have
permission to do the full read.

## Environment you can rely on

- `git`, `gh`, `jq`, `curl`
- `~/workspace` is a clean working directory
- `$TASK_ID` — the Backlite task ID
- `$PROMPT` — the URL to read (yes, just the URL — read tasks always have
  the URL as the prompt)
- `$BACKFLOW_API_BASE_URL` — base URL for Backlite's reader-only API endpoints
- `$OPENAI_API_KEY` — required by `read-embed.sh` and `read-similar.sh`
- Helper scripts in this skill bundle (also at `~/.claude/skills/read/`):
  - `read-embed.sh <text>` — embed text via OpenAI text-embedding-3-small,
    returns a JSON float array on stdout
  - `read-similar.sh <text> [n]` — find semantically similar existing
    readings (uses `read-embed.sh` then calls `/api/v1/readings/similar`)
  - `read-lookup.sh <url>` — exact-URL duplicate check via
    `/api/v1/readings/lookup` (returns a JSON array; empty = no match)

The orchestrator does its own dispatch-time duplicate-URL gate before
launching this container. `read-lookup.sh` is still available as a
best-effort hint mid-run.

## What you must do

1. **Identify the URL.** `$PROMPT` is the URL. If it doesn't look like a URL,
   write a failure `status.json` (see below) with
   `error: "prompt is not a URL"`, emit `BACKFLOW_STATUS_JSON:`, and exit.

2. **Fetch the page.** Use the `WebFetch` tool to read the URL. If the page
   is paginated or requires JS, do your best with what you can fetch.

3. **Draft the reading.** Summarize what you read into:
   - `title` — page title (string)
   - `tldr` — <= 280 chars, the single sentence you'd tell someone to
     answer "what is this article about?"
   - `tags` — lowercase slugs (e.g. `["systems", "go", "concurrency"]`)
   - `keywords` — additional searchable terms
   - `people` / `orgs` — names mentioned that someone might want to filter on
   - `summary_markdown` — a 3-6 paragraph markdown summary covering the key
     points, methodology, and conclusion

4. **Find similar existing readings.** Pipe the draft TL;DR through
   `read-similar.sh` to discover related readings:

   ```
   echo "$TLDR" | ~/.claude/skills/read/read-similar.sh
   ```

   The output is a JSON array of `{id, title, url, similarity}` objects.

5. **Judge novelty.** Set `novelty_verdict` to one of:
   - `"novel"` — no meaningful overlap with anything in the readings table
   - `"extends_existing"` — adds detail, contradiction, or follow-up to a
     known topic (cite at least one in `connections[]`)
   - `"duplicate"` — same URL or substantively identical content. The
     orchestrator usually catches duplicate URLs before dispatch, but if
     you discover the agent's draft is functionally identical to an
     existing reading, set this so the post-completion handler skips the
     write.

6. **Build connections.** From the similar-readings output, pick the
   relevant entries and record them as `connections[]`:
   `[{"reading_id": "bf_...", "reason": "<one sentence why these are related>"}]`.
   Empty array is fine if nothing related.

7. **Write `status.json`.** Use the Write tool to create
   `/home/agent/workspace/status.json` matching the schema below, then echo:

   ```
   BACKFLOW_STATUS_JSON:{...}
   ```

   matching the same JSON. Both writes are required: the marker is for live
   log streaming, the file is for the orchestrator's post-exit read.

## status.json schema (read mode)

```json
{
  "exit_code": 0,
  "complete": true,
  "needs_input": false,
  "question": "",
  "error": "",
  "task_mode": "read",
  "url": "https://example.com/post",
  "title": "Some Post Title",
  "tldr": "One-sentence summary <= 280 chars.",
  "tags": ["a", "b"],
  "keywords": ["x", "y"],
  "people": ["Person Name"],
  "orgs": ["Org Name"],
  "novelty_verdict": "novel",
  "connections": [
    {"reading_id": "bf_existing", "reason": "covers same topic from a different angle"}
  ],
  "summary_markdown": "## Summary\n\n..."
}
```

- `complete: true` only when the reading payload is filled in.
- The orchestrator re-embeds the final TL;DR using its own embeddings client
  (so the agent's draft TL;DR can be refined without re-embedding here). The
  agent does NOT need to attach an embedding.
- The entrypoint will fill in `cost_usd` after you exit; you may omit it.

## What you must NOT do

- Do not retry on soft failure. If you can't fetch the URL or build a
  meaningful summary, fail loud with a clear `error`.
- Do not write code, commit, or open PRs.
- Do not make external API calls beyond the URL fetch and the helper
  scripts.
