-- +goose Up

CREATE TABLE api_keys (
    key_hash    TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    permissions TEXT NOT NULL DEFAULT '[]',
    expires_at  TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_api_keys_expires_at ON api_keys(expires_at);

-- +goose Down

DROP TABLE IF EXISTS api_keys;
