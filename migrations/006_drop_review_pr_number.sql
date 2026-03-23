-- +goose Up
ALTER TABLE tasks DROP COLUMN IF EXISTS review_pr_number;

-- +goose Down
ALTER TABLE tasks ADD COLUMN review_pr_number INTEGER NOT NULL DEFAULT 0;
