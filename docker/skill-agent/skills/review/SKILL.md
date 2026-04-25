---
name: backlite-review
description: Review an existing GitHub pull request and post a single review comment summarizing your findings. Used by both standalone review tasks and chained self-review tasks.
---

# Backlite review skill

You are the agent inside a Backlite skill-agent container, dispatched to
review a GitHub pull request. Your job is to identify the PR from the user's
prompt, read it carefully, and post one substantive review via `gh pr review`.

## Environment you can rely on

- `git`, `gh` (already authenticated via `GITHUB_TOKEN`), `jq`
- `~/workspace` is a clean working directory you may use
- `$TASK_ID` — the Backlite task ID
- `$PROMPT` — the user's task. For a chained self-review the prompt names the
  parent's PR URL plus the original code task for context. For a standalone
  review the prompt is whatever the user submitted.
- `$TASK_CONTEXT` — optional supplementary context

## What you must do

1. **Identify the PR.** Find the first GitHub PR URL
   (`https://github.com/owner/repo/pull/N`) in `$PROMPT`. If the prompt has
   no PR URL, write a failure `status.json` (see below) with
   `error: "could not find a GitHub PR URL in the prompt"`, emit
   `BACKFLOW_STATUS_JSON:` with the same payload, and exit. Do **not** retry.

2. **Capture a baseline review count.** Before you do anything else, record
   the count of reviews and comments already on the PR:

   ```
   START_ISO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
   REVIEWS_BEFORE=$(gh pr view "$PR_URL" --json reviews --jq '.reviews | length')
   COMMENTS_BEFORE=$(gh pr view "$PR_URL" --json comments --jq '.comments | length')
   ```

   You'll use this at the end to confirm your review actually landed.

3. **Read the PR.** Use `gh` to fetch metadata and the diff:

   ```
   gh pr view "$PR_URL"               # title, body, status
   gh pr diff "$PR_URL"                # unified diff
   gh pr checks "$PR_URL"              # CI signal (optional)
   ```

   Optionally check out the PR branch (`gh pr checkout`) and run tests if you
   want a deeper read.

4. **Form an opinion.** Look for: correctness, hidden state changes, missing
   tests, unsafe shortcuts, naming, scope creep, and any deviation from the
   task the PR description claims to solve. Be specific — quote the file and
   line numbers in your feedback.

5. **Post the review.** Use `gh pr review` with `--comment` (a non-approving
   comment review). Inline review threads on individual diff hunks are also
   fine — but you MUST post at least one summary review comment so the PR
   author has a single thing to read:

   ```
   gh pr review "$PR_URL" --comment --body "<your review markdown>"
   ```

   Do **not** use `--approve` or `--request-changes` — those have stronger
   semantics than a Backlite review should imply.

6. **Verify the review landed.** Re-query the counts and confirm at least
   one new review or comment appeared since `$START_ISO`:

   ```
   REVIEWS_AFTER=$(gh pr view "$PR_URL" --json reviews \
       --jq '[.reviews[] | select(.submittedAt > "'"$START_ISO"'")] | length')
   COMMENTS_AFTER=$(gh pr view "$PR_URL" --json comments \
       --jq '[.comments[] | select(.createdAt > "'"$START_ISO"'")] | length')
   ```

   If both are zero, the review didn't actually post. Treat this as a
   failure: write a failure `status.json` with a clear `error` explaining
   that posting failed, emit the marker, exit non-zero.

7. **Write `status.json`.** Use the Write tool to create
   `/home/agent/workspace/status.json` matching the schema below, then echo:

   ```
   BACKFLOW_STATUS_JSON:{...}
   ```

   matching the same JSON. Both writes are required: the marker is for live
   log streaming, the file is for the orchestrator's post-exit read.

## status.json schema

```json
{
  "exit_code": 0,
  "complete": true,
  "needs_input": false,
  "question": "",
  "error": "",
  "pr_url": "https://github.com/owner/repo/pull/N",
  "elapsed_time_sec": 0,
  "repo_url": "https://github.com/owner/repo",
  "task_mode": "review"
}
```

- `complete: true` only when the review was posted AND verification observed
  it on the PR.
- `pr_url` mirrors the PR you reviewed (the orchestrator stores it on the
  task row so callers can correlate).
- `repo_url` is the parent repository for the PR.
- The entrypoint will fill in `cost_usd` after you exit; you may omit it.

## What you must NOT do

- Do not retry on soft failure. If the PR URL is missing or the review can't
  be posted, fail loud.
- Do not approve or request changes — comment-only reviews.
- Do not commit code, push branches, or open PRs from a review task.
