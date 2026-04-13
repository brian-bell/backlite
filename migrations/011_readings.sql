-- +goose Up

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE readings (
    id              TEXT PRIMARY KEY,
    task_id         TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    url             TEXT NOT NULL,
    title           TEXT NOT NULL DEFAULT '',
    tldr            TEXT NOT NULL DEFAULT '',
    tags            TEXT[] NOT NULL DEFAULT '{}',
    keywords        TEXT[] NOT NULL DEFAULT '{}',
    people          TEXT[] NOT NULL DEFAULT '{}',
    orgs            TEXT[] NOT NULL DEFAULT '{}',
    novelty_verdict TEXT NOT NULL DEFAULT '',
    connections     JSONB NOT NULL DEFAULT '[]',
    summary         TEXT NOT NULL DEFAULT '',
    raw_output      JSONB NOT NULL DEFAULT '{}',
    embedding       vector(1536),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_readings_url ON readings(url);
CREATE INDEX idx_readings_embedding ON readings USING hnsw (embedding vector_cosine_ops);

-- Reader API schema: the only schema exposed via Supabase PostgREST.
-- Configure Supabase dashboard → API → Exposed schemas to include "reader" only.
-- This keeps public (tasks, instances, api_keys, etc.) invisible to PostgREST.
CREATE SCHEMA reader;

-- security_invoker = true makes the view run with the caller's permissions
-- instead of the view owner's. Without this, the view owner (superuser)
-- bypasses RLS on public.readings, meaning any role with SELECT on the view
-- would see all rows regardless of RLS policies. With security_invoker, the
-- caller's own RLS policies apply — so backflow_reader sees rows allowed by
-- its policy, and roles without a policy see nothing.
CREATE VIEW reader.readings
    WITH (security_invoker = true)
    AS SELECT id, url, title, tldr FROM public.readings;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reader.match_readings(query_embedding vector(1536), match_count int)
RETURNS TABLE (id TEXT, title TEXT, tldr TEXT, url TEXT, similarity float)
LANGUAGE sql STABLE
AS $$
    SELECT
        r.id,
        r.title,
        r.tldr,
        r.url,
        1 - (r.embedding <=> query_embedding) AS similarity
    FROM public.readings r
    WHERE r.embedding IS NOT NULL
    ORDER BY r.embedding <=> query_embedding
    LIMIT match_count;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'backflow_reader') THEN
        CREATE ROLE backflow_reader NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

-- backflow_reader needs SELECT on public.readings for RLS-gated access
-- through the security_invoker view and the match_readings function.
GRANT USAGE ON SCHEMA reader TO backflow_reader;
GRANT SELECT ON public.readings TO backflow_reader;
GRANT SELECT ON reader.readings TO backflow_reader;
REVOKE EXECUTE ON FUNCTION reader.match_readings(vector(1536), int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION reader.match_readings(vector(1536), int) TO backflow_reader;

ALTER TABLE readings ENABLE ROW LEVEL SECURITY;
CREATE POLICY readings_reader_select ON readings FOR SELECT TO backflow_reader USING (true);

-- Enable RLS on existing tables (deny-all for non-owner roles).
ALTER TABLE tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE instances ENABLE ROW LEVEL SECURITY;
ALTER TABLE allowed_senders ENABLE ROW LEVEL SECURITY;
ALTER TABLE discord_installs ENABLE ROW LEVEL SECURITY;
ALTER TABLE discord_task_threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE api_keys ENABLE ROW LEVEL SECURITY;

-- Agent image tracking on tasks.
ALTER TABLE tasks ADD COLUMN agent_image TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE tasks DROP COLUMN IF EXISTS agent_image;

ALTER TABLE api_keys DISABLE ROW LEVEL SECURITY;
ALTER TABLE discord_task_threads DISABLE ROW LEVEL SECURITY;
ALTER TABLE discord_installs DISABLE ROW LEVEL SECURITY;
ALTER TABLE allowed_senders DISABLE ROW LEVEL SECURITY;
ALTER TABLE instances DISABLE ROW LEVEL SECURITY;
ALTER TABLE tasks DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS readings_reader_select ON readings;
ALTER TABLE readings DISABLE ROW LEVEL SECURITY;

REVOKE ALL ON reader.readings FROM backflow_reader;
REVOKE ALL ON public.readings FROM backflow_reader;
REVOKE EXECUTE ON FUNCTION reader.match_readings(vector(1536), int) FROM backflow_reader;
REVOKE USAGE ON SCHEMA reader FROM backflow_reader;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'backflow_reader') THEN
        DROP ROLE backflow_reader;
    END IF;
END
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS reader.match_readings(vector(1536), int);
DROP VIEW IF EXISTS reader.readings;
DROP SCHEMA IF EXISTS reader;
DROP TABLE IF EXISTS readings;
DROP EXTENSION IF EXISTS vector;
