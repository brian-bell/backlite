# SMS Setup (Twilio)

Backflow supports bidirectional SMS: create tasks by texting a phone number, and receive status notifications when tasks complete or fail.

## 1. Twilio Account Setup

1. Create a Twilio account at twilio.com
2. From the Twilio Console, grab your **Account SID** and **Auth Token**
3. Buy a phone number (or use the trial number) — note it in E.164 format (e.g. `+15551234567`)

## 2. Configure Backflow Environment Variables

Add these to your `.env`:

```bash
BACKFLOW_SMS_PROVIDER=twilio
TWILIO_ACCOUNT_SID=ACxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
TWILIO_AUTH_TOKEN=your_auth_token_here
BACKFLOW_SMS_FROM_NUMBER=+15551234567
# Optional: which events trigger SMS (defaults to task.completed,task.failed)
BACKFLOW_SMS_EVENTS=task.completed,task.failed
# Optional: disable outbound SMS while keeping inbound (defaults to true)
BACKFLOW_SMS_OUTBOUND_ENABLED=true
```

If `BACKFLOW_SMS_PROVIDER` is unset or empty, SMS is fully disabled (noop). Set `BACKFLOW_SMS_OUTBOUND_ENABLED=false` to accept inbound SMS (task creation via text) while suppressing outbound notifications.

## 3. Register Allowed Senders

Inbound SMS (creating tasks via text) requires pre-authorized senders in the `allowed_senders` table. Insert rows directly in Postgres:

```sql
INSERT INTO allowed_senders (channel_type, address, enabled, created_at)
VALUES ('sms', '+15559876543', true, now());
```

- `address` — the sender's phone number in E.164 format
- `enabled` — set to `false` to revoke access without deleting

## 4. Configure Twilio Inbound Webhook

In the Twilio Console, set the webhook URL for your phone number's **"A Message Comes In"** setting to:

```
https://your-backflow-host/webhooks/sms/inbound   (POST)
```

This endpoint receives incoming texts, authorizes the sender, parses the message for a repo URL and prompt, and creates a task.

## 5. How It Works

**Inbound (SMS to Task):** Text your Backflow number with a message like:

- `Fix the login bug` — uses sender's default repo
- `github.com/org/repo Fix the login bug` — explicit repo
- `Implement the issue https://github.com/org/repo/issues/123` — issue URL infers the repo automatically
- `https://github.com/org/repo/pull/42` — PR URL auto-detects review mode (sets `task_mode=review`, infers repo)
- `Review https://github.com/org/repo/pull/42 for security issues` — PR URL auto-detects review mode, remaining text becomes the review prompt

The task is created with a `reply_channel` of `sms:+15559876543` so results go back to you.

**Outbound (Task to SMS):** When a task reaches a matching event (e.g. `task.completed`), Backflow sends an SMS to the reply channel:

- "Task bf_xxx completed. PR: https://github.com/org/repo/pull/42"
- "Task bf_xxx failed. Some error message"

## 6. Deployment Notes

- **A2P 10DLC registration is required** for outbound SMS to US numbers. Twilio will block or filter application-to-person messages from unregistered 10-digit long codes. Register your brand and campaign in the Twilio Console under **Messaging > Trust Hub > A2P 10DLC** before going to production.
- Twilio webhooks require a **publicly reachable URL** — use `cloudflared tunnel --url http://localhost:8080` for local dev (no account needed, see the [Local Tunnel](../README.md#local-tunnel-for-webhooks) section in the README)
- The Twilio integration uses raw HTTP (no SDK dependency), with 3 retries and exponential backoff
- `max_subscription` auth mode runs one agent at a time, so inbound SMS tasks queue up serially
- `max_subscription` is not supported in `fargate` mode
