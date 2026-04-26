-- +goose Up

ALTER TABLE tasks ADD COLUMN inline_content_sha256 TEXT NULL;

-- +goose Down

ALTER TABLE tasks DROP COLUMN inline_content_sha256;
