-- +goose Up

CREATE TABLE tasks (
    id               TEXT PRIMARY KEY,
    status           TEXT NOT NULL DEFAULT 'pending',
    task_mode        TEXT NOT NULL DEFAULT 'auto',
    harness          TEXT NOT NULL DEFAULT 'claude_code',
    repo_url         TEXT NOT NULL DEFAULT '',
    branch           TEXT NOT NULL DEFAULT '',
    target_branch    TEXT NOT NULL DEFAULT '',
    prompt           TEXT NOT NULL,
    context          TEXT NOT NULL DEFAULT '',
    model            TEXT NOT NULL DEFAULT '',
    effort           TEXT NOT NULL DEFAULT '',
    max_budget_usd   REAL NOT NULL DEFAULT 0,
    max_runtime_sec  INTEGER NOT NULL DEFAULT 0,
    max_turns        INTEGER NOT NULL DEFAULT 0,
    create_pr        BOOLEAN NOT NULL DEFAULT false,
    self_review      BOOLEAN NOT NULL DEFAULT false,
    save_agent_output BOOLEAN NOT NULL DEFAULT true,
    pr_title         TEXT NOT NULL DEFAULT '',
    pr_body          TEXT NOT NULL DEFAULT '',
    pr_url           TEXT NOT NULL DEFAULT '',
    output_url       TEXT NOT NULL DEFAULT '',
    allowed_tools    TEXT NOT NULL DEFAULT '[]',
    claude_md        TEXT NOT NULL DEFAULT '',
    env_vars         TEXT NOT NULL DEFAULT '{}',
    instance_id      TEXT NOT NULL DEFAULT '',
    container_id     TEXT NOT NULL DEFAULT '',
    retry_count      INTEGER NOT NULL DEFAULT 0,
    user_retry_count INTEGER NOT NULL DEFAULT 0,
    cost_usd         REAL NOT NULL DEFAULT 0,
    elapsed_time_sec INTEGER NOT NULL DEFAULT 0,
    error            TEXT NOT NULL DEFAULT '',
    ready_for_retry  BOOLEAN NOT NULL DEFAULT false,
    reply_channel    TEXT NOT NULL DEFAULT '',
    agent_image      TEXT NOT NULL DEFAULT '',
    force            BOOLEAN NOT NULL DEFAULT false,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    started_at       TEXT,
    completed_at     TEXT
);

CREATE INDEX idx_tasks_status ON tasks(status);
CREATE INDEX idx_tasks_created ON tasks(created_at);

CREATE TABLE instances (
    instance_id        TEXT PRIMARY KEY,
    instance_type      TEXT NOT NULL,
    availability_zone  TEXT NOT NULL DEFAULT '',
    private_ip         TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'pending',
    max_containers     INTEGER NOT NULL DEFAULT 4,
    running_containers INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_instances_status ON instances(status);

CREATE TABLE allowed_senders (
    channel_type TEXT NOT NULL,
    address      TEXT NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT true,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (channel_type, address)
);

-- +goose Down

DROP TABLE IF EXISTS allowed_senders;
DROP TABLE IF EXISTS instances;
DROP TABLE IF EXISTS tasks;
