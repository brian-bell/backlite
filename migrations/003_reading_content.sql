-- +goose Up

ALTER TABLE readings ADD COLUMN content_type      TEXT    NOT NULL DEFAULT '';
ALTER TABLE readings ADD COLUMN content_status    TEXT    NOT NULL DEFAULT '';
ALTER TABLE readings ADD COLUMN content_bytes     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE readings ADD COLUMN extracted_bytes   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE readings ADD COLUMN content_sha256    TEXT    NOT NULL DEFAULT '';
ALTER TABLE readings ADD COLUMN fetched_at        TEXT;

-- +goose Down

ALTER TABLE readings DROP COLUMN fetched_at;
ALTER TABLE readings DROP COLUMN content_sha256;
ALTER TABLE readings DROP COLUMN extracted_bytes;
ALTER TABLE readings DROP COLUMN content_bytes;
ALTER TABLE readings DROP COLUMN content_status;
ALTER TABLE readings DROP COLUMN content_type;
