---
name: backlite-auto
description: Inspect the prompt, decide whether the user wants code work or PR review, and dispatch to the matching skill. Used when the orchestrator could not pre-resolve the mode.
---

# Backlite auto skill

You are the agent inside a Backlite skill-agent container, dispatched in
**auto mode**. Auto means the orchestrator does not yet know whether the
user wants you to write code or review an existing PR. Your first job is
to decide, then run the matching skill end-to-end.

## How to decide

Look at `$PROMPT`:

- If it contains a GitHub PR URL of the form
  `https://github.com/<owner>/<repo>/pull/<n>` **and** the prompt is
  asking for review/feedback/comments on that PR (not asking you to
  change it), this is a **review** task.
- Otherwise (changes requested, bug fix, new feature, refactor, anything
  that requires writing code, or no URL at all) it is a **code** task.

Edge cases:
- PR URL plus "fix the failing test in this PR" → **code** (you are
  being asked to make changes).
- PR URL plus "what do you think?" / "any feedback?" / "review this" →
  **review**.

## What to do next

Once you've decided:

1. Read the matching skill via the Read tool:
   - code: `~/.claude/skills/code/SKILL.md`
   - review: `~/.claude/skills/review/SKILL.md`
2. Follow it end-to-end, including writing `status.json` and emitting
   the `BACKFLOW_STATUS_JSON:` marker line.
3. In the final `status.json`, set `task_mode` to the **resolved**
   concrete mode (`code` or `review`). Do not leave it as `auto`.

## What you must NOT do

- Do not write `task_mode: "auto"` in `status.json`. Auto is a routing
  hint, not a final outcome — the orchestrator stores this value and
  uses it to decide downstream behavior (e.g., self-review chaining
  only fires for resolved code tasks).
- Do not try to handle both modes inline. Loading the right skill
  keeps each per-mode contract testable.
- Do not retry on soft failure. If the prompt is unworkable, write a
  failure `status.json` (with the resolved mode you would have used)
  and exit non-zero.
