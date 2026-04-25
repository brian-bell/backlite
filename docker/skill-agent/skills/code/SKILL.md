---
name: backlite-code
description: Implement a coding task in a fresh git clone, commit, push, and open a PR. The orchestrator routes claude_code code-mode tasks to this skill via the skill-agent image.
---

# Backlite code skill

You are the agent inside a Backlite skill-agent container. The orchestrator
launched you with a starter prompt that includes the user's task. Your job is
to take that task end-to-end: clone the repo, do the work, push a branch, open
a PR, and write a final `status.json` describing the outcome.

## Environment you can rely on

- `git`, `gh` (already authenticated via `GITHUB_TOKEN`), `jq`
- `~/workspace` is a clean working directory you may use
- `$TASK_ID` — the Backlite task ID (use it in branch names so they're traceable)
- `$PROMPT` — the user's task (also passed to you in the starter prompt)
- `$CLAUDE_MD` — optional project context the user supplied; if non-empty,
  append it to the cloned repo's `CLAUDE.md` (or create one) before starting work
- `$TASK_CONTEXT` — optional supplementary context from the user
- `$CREATE_PR` — `true` when the user wants a PR opened, `false` to stop after push
- `$PR_TITLE`, `$PR_BODY` — optional user-supplied PR metadata; otherwise generate them
- `$BACKFLOW_API_BASE_URL` — only needed for read-mode helpers (not used here)

## What you must do

1. **Identify the repo.** Find the first GitHub repo URL in `$PROMPT`. If the
   prompt has no usable GitHub URL, write a failure `status.json` (see below)
   with `error: "could not find a GitHub repo URL in the prompt"`, emit
   `BACKFLOW_STATUS_JSON:` with the same payload, and exit. Do **not** retry.

2. **Identify the target branch.** Default to `main` unless the prompt names a
   different base branch. Verify the branch exists with
   `gh api repos/<owner>/<repo>/branches/<branch>` and fall back to the
   repository's default branch if it doesn't.

3. **Clone.** `cd ~/workspace && git clone <repo_url>` and `cd` into the repo.

4. **Inject context.** If `$CLAUDE_MD` is non-empty, append its contents to
   `CLAUDE.md` (creating the file if needed). Do not commit this change as a
   separate commit — it's just runtime context for you.

5. **Work.** Implement the change described in `$PROMPT`. Run the project's
   tests if a test command is obvious. If you discover the request is
   ambiguous or impossible, do not retry — write a failure `status.json` with
   a clear error and exit.

6. **Commit.** Create a branch named `backlite/${TASK_ID}` (or similar — keep
   it traceable). Stage your changes with named paths (avoid `git add -A`),
   commit with a short message that focuses on *why*. Push the branch.

7. **Open a PR if asked.** If `$CREATE_PR=true`, open a PR via `gh pr create`
   targeting the target branch. Use `$PR_TITLE` if non-empty, otherwise
   generate a concise title. Use `$PR_BODY` if non-empty, otherwise draft a
   short body summarizing the change.

8. **Write `status.json`.** Use the Write tool to create
   `/home/agent/workspace/status.json` with the schema below, then echo a
   single line:

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
  "target_branch": "main",
  "task_mode": "code"
}
```

- `complete: true` when you finished the work and pushed (and opened a PR if
  requested). Otherwise `false`.
- `needs_input: true` only when you stopped because the user must clarify
  something — set `question` to the prompt for the user.
- `error` should describe what went wrong if `complete=false` and
  `needs_input=false`.
- `pr_url` is the new PR's URL when `$CREATE_PR=true`. Empty otherwise.
- `repo_url` and `target_branch` are what you actually used (the orchestrator
  stores these so they survive into the task record).
- The entrypoint will fill in `cost_usd` after you exit; you may omit it.

## What you must NOT do

- Do not retry on soft failure. If the user's prompt is unworkable, fail loud.
- Do not run as root, install global packages, or modify files outside
  `~/workspace`.
- Do not commit secrets. `GITHUB_TOKEN`, `ANTHROPIC_API_KEY`, etc. live in
  process env, never write them to disk.
