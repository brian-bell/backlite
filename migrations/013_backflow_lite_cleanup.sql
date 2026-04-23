-- +goose Up

DROP TABLE IF EXISTS discord_task_threads;
DROP TABLE IF EXISTS discord_installs;
DROP TABLE IF EXISTS allowed_senders;

-- +goose Down

-- No-op: these tables are orphaned from removed integrations and are not restored.
