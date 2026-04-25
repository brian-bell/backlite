-- +goose Up

ALTER TABLE tasks ADD COLUMN parent_task_id TEXT;
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);

-- +goose Down

DROP INDEX IF EXISTS idx_tasks_parent_task_id;
ALTER TABLE tasks DROP COLUMN parent_task_id;
