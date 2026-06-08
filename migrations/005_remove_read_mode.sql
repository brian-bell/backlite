-- +goose Up

DROP INDEX IF EXISTS idx_readings_url;
DROP TABLE IF EXISTS readings;

CREATE TABLE tasks_new (
    id                TEXT PRIMARY KEY,
    status            TEXT NOT NULL DEFAULT 'pending',
    task_mode         TEXT NOT NULL DEFAULT 'auto',
    harness           TEXT NOT NULL DEFAULT 'claude_code',
    repo_url          TEXT NOT NULL DEFAULT '',
    branch            TEXT NOT NULL DEFAULT '',
    target_branch     TEXT NOT NULL DEFAULT '',
    prompt            TEXT NOT NULL,
    context           TEXT NOT NULL DEFAULT '',
    model             TEXT NOT NULL DEFAULT '',
    effort            TEXT NOT NULL DEFAULT '',
    max_budget_usd    REAL NOT NULL DEFAULT 0,
    max_runtime_sec   INTEGER NOT NULL DEFAULT 0,
    max_turns         INTEGER NOT NULL DEFAULT 0,
    create_pr         BOOLEAN NOT NULL DEFAULT false,
    self_review       BOOLEAN NOT NULL DEFAULT false,
    save_agent_output BOOLEAN NOT NULL DEFAULT true,
    pr_title          TEXT NOT NULL DEFAULT '',
    pr_body           TEXT NOT NULL DEFAULT '',
    pr_url            TEXT NOT NULL DEFAULT '',
    output_url        TEXT NOT NULL DEFAULT '',
    allowed_tools     TEXT NOT NULL DEFAULT '[]',
    claude_md         TEXT NOT NULL DEFAULT '',
    env_vars          TEXT NOT NULL DEFAULT '{}',
    container_id      TEXT NOT NULL DEFAULT '',
    retry_count       INTEGER NOT NULL DEFAULT 0,
    user_retry_count  INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL NOT NULL DEFAULT 0,
    elapsed_time_sec  INTEGER NOT NULL DEFAULT 0,
    error             TEXT NOT NULL DEFAULT '',
    ready_for_retry   BOOLEAN NOT NULL DEFAULT false,
    agent_image       TEXT NOT NULL DEFAULT '',
    parent_task_id    TEXT REFERENCES tasks_new(id) ON DELETE SET NULL,
    created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at        TEXT,
    completed_at      TEXT
);

INSERT INTO tasks_new (
    id, status, task_mode, harness, repo_url, branch, target_branch,
    prompt, context,
    model, effort, max_budget_usd, max_runtime_sec, max_turns,
    create_pr, self_review, save_agent_output, pr_title, pr_body, pr_url, output_url,
    allowed_tools, claude_md, env_vars,
    container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
    ready_for_retry, agent_image, parent_task_id,
    created_at, updated_at, started_at, completed_at
)
SELECT
    id, status, task_mode, harness, repo_url, branch, target_branch,
    prompt, context,
    model, effort, max_budget_usd, max_runtime_sec, max_turns,
    create_pr, self_review, save_agent_output, pr_title, pr_body, pr_url, output_url,
    allowed_tools, claude_md, env_vars,
    container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
    ready_for_retry, agent_image,
    CASE
        WHEN parent_task_id IN (SELECT id FROM tasks WHERE task_mode != 'read') THEN parent_task_id
        ELSE NULL
    END,
    created_at, updated_at, started_at, completed_at
FROM tasks
WHERE task_mode != 'read';

DROP TABLE tasks;
ALTER TABLE tasks_new RENAME TO tasks;

CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_created ON tasks(created_at);
CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);

-- +goose Down
-- Intentionally left blank: this migration removes an unsupported feature.
