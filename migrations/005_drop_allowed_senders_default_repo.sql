-- +goose Up
ALTER TABLE allowed_senders DROP COLUMN IF EXISTS default_repo;

-- +goose Down
ALTER TABLE allowed_senders ADD COLUMN default_repo TEXT NOT NULL DEFAULT '';
