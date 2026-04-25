# Resend setup for email summary delivery

When configured, the read skill emails a plain-text summary of each completed
read-mode task to a fixed inbox. The skill bundle owns the sending; the
orchestrator's job is just to propagate three env vars into the skill-agent
container.

This guide walks through the operator-side prerequisites. None of it is
optional — Resend rejects sends from unverified domains, and Backflow's
startup gate blocks partial configurations.

## Scope

- Read mode only (`task_mode: "read"`).
- `claude_code` harness only.
- Skill-agent image only (`BACKFLOW_SKILL_AGENT_IMAGE` set).

Codex read tasks (which route to `docker/reader/`) and non-read modes do not
send email in this slice. Container-level failures where the skill never
runs (image pull failure, OOM kill, timeout) also do not send email — use
the existing webhook + DB for those signals.

## 1. Create a Resend account and API key

1. Sign up at <https://resend.com>.
2. In the dashboard, create an API key with "Sending access".
3. Copy the key (it begins with `re_`). This is the value of
   `BACKFLOW_RESEND_API_KEY`.

## 2. Verify a sender domain

The `From` address must use a domain you own and have verified with Resend.
The Resend sandbox sender (`onboarding@resend.dev`) is **not** supported —
Resend will reject the send and the email will silently fail (the read task
itself still completes).

1. In the Resend dashboard, go to **Domains → Add Domain** and enter the
   domain you want to send from (e.g., `mail.example.com`).
2. Resend shows you a set of DNS records to publish — typically:
   - one SPF `TXT` record,
   - two or three DKIM `CNAME` records,
   - optionally a return-path `CNAME` for bounce tracking.
3. Add those records at your DNS provider. Wait for Resend to mark the
   domain as **Verified** (usually a few minutes; can be longer depending on
   your DNS TTLs).

The address you set as `BACKFLOW_NOTIFY_EMAIL_FROM` (e.g.,
`backflow@mail.example.com`) must use this verified domain. The local part
(before the `@`) is yours to choose.

## 3. Pick a recipient

`BACKFLOW_NOTIFY_EMAIL_TO` is just an inbox you control. No DNS work is
required for the recipient.

## 4. Set the env vars

The three vars are read at startup. See `internal/config/config.go` for the
authoritative list and any current defaults.

- `BACKFLOW_RESEND_API_KEY` — the `re_…` key from step 1.
- `BACKFLOW_NOTIFY_EMAIL_FROM` — `Name <local@verified-domain>` or just
  `local@verified-domain`. Must contain `@`.
- `BACKFLOW_NOTIFY_EMAIL_TO` — recipient address. Must contain `@`.

All three must be set together. If you set one or two but not all three,
`config.Load()` fails at startup with a named-var error, so you find the
typo before you wonder why no email arrived.

Inside the skill-agent container these vars are available without the
`BACKFLOW_` prefix (matching how `OPENAI_API_KEY` is propagated):
`RESEND_API_KEY`, `NOTIFY_EMAIL_FROM`, `NOTIFY_EMAIL_TO`. They are reserved
names — per-task `env_vars` cannot override them.

## 5. Verify delivery

1. Submit a read task:
   ```bash
   curl -X POST localhost:8080/api/v1/tasks \
       -H "Authorization: Bearer $BACKFLOW_API_KEY" \
       -H "Content-Type: application/json" \
       -d '{"task_mode":"read","prompt":"https://example.com","harness":"claude_code"}'
   ```
2. Wait for the `task.completed` webhook (or poll `GET /api/v1/tasks/{id}`).
3. Check the `BACKFLOW_NOTIFY_EMAIL_TO` inbox — you should see an email
   whose subject is the page title and whose body contains the URL, title,
   TL;DR, and a `Task: bf_…` line.

If the email is missing:

- Check the orchestrator logs for the skill-agent container's stderr — the
  script logs `send-email: …` lines on both success and failure.
- Confirm the sender domain is **Verified** (not "Pending") in the Resend
  dashboard.
- Check Resend's **Logs** view for rejected sends.
