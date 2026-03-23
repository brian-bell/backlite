-- +goose Up
ALTER TABLE tasks ALTER COLUMN task_mode SET DEFAULT 'auto';
ALTER TABLE tasks ALTER COLUMN repo_url SET DEFAULT '';

-- +goose Down
ALTER TABLE tasks ALTER COLUMN task_mode SET DEFAULT 'code';
ALTER TABLE tasks ALTER COLUMN repo_url DROP DEFAULT;
