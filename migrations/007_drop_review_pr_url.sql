-- +goose Up
ALTER TABLE tasks DROP COLUMN IF EXISTS review_pr_url;

-- +goose Down
ALTER TABLE tasks ADD COLUMN review_pr_url TEXT NOT NULL DEFAULT '';
