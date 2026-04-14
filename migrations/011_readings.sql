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
    is_available    BOOLEAN NOT NULL DEFAULT true,
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
-- caller's own RLS policies apply — so anon sees rows allowed by its policy.
CREATE VIEW reader.readings
    WITH (security_invoker = true)
    AS SELECT id, url, title, tldr FROM public.readings;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION reader.match_readings(query_embedding vector(1536), match_count int)
RETURNS TABLE (id TEXT, title TEXT, tldr TEXT, url TEXT, similarity float)
LANGUAGE sql STABLE
-- PostgREST runs the function with search_path set to the request schema
-- (reader), which hides the pgvector `<=>` operator living on public.vector.
-- Pinning search_path here keeps the operator resolvable.
SET search_path = reader, public, extensions
AS $$
    SELECT
        r.id,
        r.title,
        r.tldr,
        r.url,
        1 - (r.embedding <=> query_embedding) AS similarity
    FROM public.readings r
    WHERE r.embedding IS NOT NULL
      AND r.is_available = true
    ORDER BY r.embedding <=> query_embedding
    LIMIT match_count;
$$;
-- +goose StatementEnd

-- Reader access is granted to the built-in Supabase `anon` role so requests
-- authenticated with the project's publishable key (sb_publishable_...) can
-- read through PostgREST. Writes remain blocked by RLS + lack of grants.
-- Supabase provisions `anon` automatically; bare Postgres (e.g. test
-- containers) does not, so create it here if missing.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'anon') THEN
        CREATE ROLE anon NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA reader TO anon;
GRANT SELECT ON public.readings TO anon;
GRANT SELECT ON reader.readings TO anon;
REVOKE EXECUTE ON FUNCTION reader.match_readings(vector(1536), int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION reader.match_readings(vector(1536), int) TO anon;

ALTER TABLE readings ENABLE ROW LEVEL SECURITY;
CREATE POLICY readings_anon_select ON readings FOR SELECT TO anon USING (is_available = true);

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

DROP POLICY IF EXISTS readings_anon_select ON readings;
ALTER TABLE readings DISABLE ROW LEVEL SECURITY;

REVOKE ALL ON reader.readings FROM anon;
REVOKE ALL ON public.readings FROM anon;
REVOKE EXECUTE ON FUNCTION reader.match_readings(vector(1536), int) FROM anon;
REVOKE USAGE ON SCHEMA reader FROM anon;

DROP FUNCTION IF EXISTS reader.match_readings(vector(1536), int);
DROP VIEW IF EXISTS reader.readings;
DROP SCHEMA IF EXISTS reader;
DROP TABLE IF EXISTS readings;
DROP EXTENSION IF EXISTS vector;
