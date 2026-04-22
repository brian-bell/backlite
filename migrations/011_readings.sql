-- +goose Up

CREATE TABLE readings (
    id              TEXT PRIMARY KEY,
    task_id         TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    url             TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    tldr            TEXT NOT NULL DEFAULT '',
    tags            TEXT NOT NULL DEFAULT '[]',
    keywords        TEXT NOT NULL DEFAULT '[]',
    people          TEXT NOT NULL DEFAULT '[]',
    orgs            TEXT NOT NULL DEFAULT '[]',
    novelty_verdict TEXT NOT NULL DEFAULT '',
    connections     TEXT NOT NULL DEFAULT '[]',
    summary         TEXT NOT NULL DEFAULT '',
    raw_output      TEXT NOT NULL DEFAULT '{}',
    embedding       TEXT NOT NULL DEFAULT '',
    is_available    BOOLEAN NOT NULL DEFAULT true,
    created_at      TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE UNIQUE INDEX idx_readings_url ON readings(url);

-- +goose Down

DROP TABLE IF EXISTS readings;
