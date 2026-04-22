-- +goose Up
CREATE TABLE discord_installs (
    guild_id       TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL,
    channel_id     TEXT NOT NULL,
    allowed_roles  TEXT NOT NULL DEFAULT '[]',
    installed_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE IF EXISTS discord_installs;
