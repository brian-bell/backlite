-- +goose Up
ALTER TABLE tasks ADD COLUMN ready_for_retry BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tasks ADD COLUMN user_retry_count INTEGER NOT NULL DEFAULT 0;

-- Backfill: existing terminal tasks that have been cleaned up (no container)
-- are immediately retryable, provided they are under the default retry cap.
UPDATE tasks SET ready_for_retry = true
WHERE status IN ('failed', 'cancelled', 'interrupted')
  AND container_id = ''
  AND retry_count < 2;

-- +goose Down
ALTER TABLE tasks DROP COLUMN user_retry_count;
ALTER TABLE tasks DROP COLUMN ready_for_retry;
