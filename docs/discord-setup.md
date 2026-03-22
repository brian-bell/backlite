# Discord Bot Setup

Backflow includes a Discord integration that receives interactions via webhook and delivers task lifecycle notifications to a Discord channel. This guide walks through creating a Discord application, configuring the bot, and connecting it to Backflow.

## 1. Create a Discord Application

1. Go to the [Discord Developer Portal](https://discord.com/developers/applications)
2. Click **New Application**, give it a name (e.g. "Backflow"), and create it
3. On the **General Information** page, note these values:
   - **Application ID** — used as `BACKFLOW_DISCORD_APP_ID`
   - **Public Key** — used as `BACKFLOW_DISCORD_PUBLIC_KEY` (hex-encoded Ed25519 key)

## 2. Create the Bot User

1. Go to the **Bot** tab in the left sidebar
2. Click **Reset Token** to generate a bot token — copy it immediately (you won't see it again)
   - This becomes `BACKFLOW_DISCORD_BOT_TOKEN`
3. Under **Privileged Gateway Intents**, no special intents are required for the initial integration

## 3. Invite the Bot to Your Server

Build an OAuth2 URL to install the bot with the permissions it needs:

1. Go to **OAuth2 > URL Generator**
2. Select scopes: `bot`, `applications.commands`
3. Select bot permissions: `Send Messages`, `Send Messages in Threads`, `Create Public Threads`, `Embed Links`, `Read Message History`
4. Copy the generated URL and open it in your browser
5. Select your target server and authorize

After authorizing, note the **Server ID** (right-click the server name > Copy Server ID with Developer Mode enabled) — this becomes `BACKFLOW_DISCORD_GUILD_ID`.

Also note the **Channel ID** of the channel where you want notifications (right-click the channel > Copy Channel ID) — this becomes `BACKFLOW_DISCORD_CHANNEL_ID`.

## 4. Configure Backflow Environment Variables

Add these to your `.env`:

```bash
BACKFLOW_DISCORD_APP_ID=123456789012345678
BACKFLOW_DISCORD_PUBLIC_KEY=abc123def456...  # 64-char hex string from Developer Portal
BACKFLOW_DISCORD_BOT_TOKEN=Bot MTIz...       # bot token from step 2
BACKFLOW_DISCORD_GUILD_ID=987654321098765432
BACKFLOW_DISCORD_CHANNEL_ID=111222333444555666
```

Optional:

```bash
# Comma-separated Discord role IDs that can run mutation commands (cancel, retry).
# If unset, mutation authorization is not enforced.
BACKFLOW_DISCORD_ALLOWED_ROLES=role-id-1,role-id-2

# Comma-separated event filter. If unset, all lifecycle events are delivered.
BACKFLOW_DISCORD_EVENTS=task.completed,task.failed
```

If `BACKFLOW_DISCORD_APP_ID` is unset or empty, the entire Discord integration is disabled.

## 5. Set the Interactions Endpoint URL

Discord sends slash commands and other interactions to a webhook URL that you configure in the Developer Portal:

1. Go to **General Information** in your application settings
2. Set **Interactions Endpoint URL** to:
   ```
   https://your-backflow-host/webhooks/discord
   ```
3. Discord will send a PING request to verify the endpoint — Backflow responds with PONG automatically
4. Click **Save Changes** — Discord will confirm the URL is valid

The endpoint must be publicly reachable over HTTPS. For local development, use `cloudflared tunnel --url http://localhost:8080` (no account needed).

## 6. How It Works

**Interaction verification:** Every incoming request is verified using Ed25519 signature checking against the public key. Requests with invalid or missing signatures are rejected with 401.

**Install state persistence:** At startup, Backflow writes the Discord configuration to the `discord_installs` table in PostgreSQL. This ensures the integration survives service restarts without losing context about which guild/channel to target.

**Slash command registration:** At startup, Backflow registers a `/backflow` slash command via the Discord bulk-overwrite endpoint. This happens automatically when `BACKFLOW_DISCORD_APP_ID` is set — no manual command creation is needed in the Developer Portal.

**Event notifications:** Backflow subscribes a Discord notifier to the event bus. When task lifecycle events fire (`task.created`, `task.running`, `task.completed`, etc.), the notifier receives them. Event filtering via `BACKFLOW_DISCORD_EVENTS` works the same as webhook and SMS filters — `nil` means all events, a CSV list restricts delivery.

**Current limitations:** The Discord integration is in its foundation phase. Interaction routing currently returns a deferred acknowledgment for all commands — full slash command responses, modals, buttons, and thread-based notifications will be added in future releases.

## 7. Deployment Notes

- The bot token is a secret — treat it like an API key. It is stored in environment variables only, never persisted to the database.
- The integration runs inside the Backflow service process — there is no separate bot worker to deploy.
- The launch version supports a single Discord server and a single notification channel.
- Notification delivery failures are logged but never block task processing or the orchestration loop.
- `BACKFLOW_DISCORD_PUBLIC_KEY` must be a valid 64-character hex string (32 bytes decoded). Backflow validates this at startup and exits with a fatal error if the key is malformed.
