-- +goose Up
ALTER TABLE tasks ADD COLUMN max_runtime_sec INTEGER NOT NULL DEFAULT 0;
UPDATE tasks SET max_runtime_sec = max_runtime_min * 60;
ALTER TABLE tasks DROP COLUMN max_runtime_min;

-- +goose Down
ALTER TABLE tasks ADD COLUMN max_runtime_min INTEGER NOT NULL DEFAULT 0;
UPDATE tasks SET max_runtime_min = max_runtime_sec / 60;
ALTER TABLE tasks DROP COLUMN max_runtime_sec;
