# Supabase Setup

Backflow uses **one Supabase project** — one Postgres database — accessed two different ways depending on who's talking to it. Keeping everything in a single project is what lets us stay on Supabase's free tier.

1. **Direct Postgres, via the session pooler.** The Backflow server (and `goose` migrations) connect straight to Postgres using `BACKFLOW_DATABASE_URL`. This is the canonical path for everything the application reads or writes — tasks, instances, api_keys, readings, embeddings, etc. No Supabase-specific libraries are involved; it's just `pgx`.
2. **Data API (PostgREST), via HTTPS.** The reader agent container (`docker/reader/`) is deliberately lean — no Postgres driver, no `psql`, just `curl` + `jq`. Rather than shipping a DB client, it talks to the same database through Supabase's built-in Data API (PostgREST), using the project's publishable key. Only a narrow, read-only slice of the schema is exposed this way.

The reader container's data needs are modest (look up a URL, run a similarity search) so exposing just that slice via PostgREST is cheaper and smaller than adding a DB client to the image. Both paths read and write the same rows — there's one source of truth, and nothing is replicated.

This document covers both paths, with the bulk of it focused on the Data API configuration because that's where most of the moving parts live.

## 1. Create the project

1. Create a Supabase project at [supabase.com](https://supabase.com/dashboard).
2. Note the **project ref** (`<ref>` in `https://<ref>.supabase.co`). You'll see it in the URL.
3. Under **Project Settings → Database → Connection string → Session pooler**, copy the pooler URL. Set in `.env`:
   ```bash
   BACKFLOW_DATABASE_URL=postgresql://postgres.<ref>:<password>@aws-0-<region>.pooler.supabase.com:5432/postgres
   ```
4. Enable the `vector` extension (pgvector). It's installed automatically by migration `011_readings.sql`, but Supabase needs to have the extension available. New Supabase projects do by default.

## 2. Apply migrations

From the repo root with `.env` sourced:

```bash
goose -dir migrations postgres "$BACKFLOW_DATABASE_URL" up
```

Migration `011_readings.sql` is where the reader-specific schema lives. See section 4 below for what it does and why.

## 3. Keys (and why you only need one)

Supabase currently ships **two generations** of API keys and is in the middle of a migration period:

| Key | Format | Role | Privileges | Use |
|-----|--------|------|------------|-----|
| Publishable (new) | `sb_publishable_...` | Acts as `anon` | Low (RLS-gated) | **Safe to expose** — web pages, mobile apps, source code, the reader container |
| Secret (new) | `sb_secret_...` | Bypasses RLS | High | Backend-only, full project access |
| `anon` (legacy) | JWT | `anon` | Low | Being phased out; same semantics as publishable |
| `service_role` (legacy) | JWT | bypasses RLS | High | Being phased out; same semantics as secret |

See [Supabase discussion #29260](https://github.com/orgs/supabase/discussions/29260) for the full migration schedule.

**For Backflow's reader, the only key you ever need is the publishable key**, set as `SUPABASE_ANON_KEY` in `.env`:

```bash
SUPABASE_URL=https://<ref>.supabase.co
SUPABASE_ANON_KEY=sb_publishable_...
```

### Why not the secret key?

It would work (it bypasses RLS and therefore all the grant work in migration 011 is moot), but it hands the reader container full read/write access to the entire project. The reader only needs read access to `readings` and nothing else — scope the credential accordingly.

### Why not a custom JWT for `backflow_reader` or similar?

Supabase has moved to asymmetric JWT signing (ES256). The JWKS published at `<project>.supabase.co/auth/v1/.well-known/jwks.json` contains **only Supabase-owned public keys**; the corresponding private keys are held by Supabase and never exposed in the dashboard. This means you **cannot mint your own JWTs** that PostgREST will accept — not with the "JWT Signing Keys" UI (which only rotates Supabase's own signing key), not with the legacy HS256 secret (no longer in the active key set on new projects).

Symptoms if you try:
- Custom HS256 JWT → `PGRST301: No suitable key or wrong key type`.
- Publishable/secret key in `Authorization: Bearer` → rejected; these tokens are opaque, not JWTs.

The design that does work is what migration 011 implements: grant the needed read paths to the built-in `anon` role, then authenticate with the publishable key.

### Why not use the publishable key in the `Authorization` header too?

The publishable key isn't a JWT. Supabase's gateway allows it in `Authorization: Bearer` only as a backward-compat pass-through when the value exactly matches `apikey`. The reader scripts just send `apikey:` and omit `Authorization`, which is the clean path.

## 4. The `reader` schema (migration 011)

The migration does four things:

### a) Core `readings` table in `public`

Standard Backflow table — owned by the application, used by the orchestrator and agent directly via `BACKFLOW_DATABASE_URL`. RLS is enabled; no role has read/write access by default.

### b) A `reader` schema with a minimal view

```sql
CREATE SCHEMA reader;

CREATE VIEW reader.readings
    WITH (security_invoker = true)
    AS SELECT id, url, title, tldr FROM public.readings;
```

The view exposes **only** `id`, `url`, `title`, `tldr` — hiding `embedding`, `raw_output`, `summary`, etc. from the Data API. `security_invoker = true` ensures RLS policies on the underlying table apply to the caller, not the view owner (without this, a superuser-owned view bypasses RLS silently).

### c) A `match_readings` RPC for semantic search

```sql
CREATE FUNCTION reader.match_readings(query_embedding vector(1536), match_count int)
RETURNS TABLE (id TEXT, title TEXT, tldr TEXT, url TEXT, similarity float)
LANGUAGE sql STABLE
SET search_path = reader, public, extensions
AS $$
    SELECT r.id, r.title, r.tldr, r.url,
           1 - (r.embedding <=> query_embedding) AS similarity
    FROM public.readings r
    WHERE r.embedding IS NOT NULL
    ORDER BY r.embedding <=> query_embedding
    LIMIT match_count;
$$;
```

Two subtle but important pieces:

- **`SET search_path = reader, public, extensions`**. PostgREST invokes functions with `search_path` set to the request's schema (`reader`). The `<=>` operator lives on `public.vector` (pgvector), so without this setting the function fails at runtime with `operator does not exist: public.vector <=> public.vector`. Pinning search_path keeps the operator resolvable.
- **`STABLE`** — the function always returns the same result for the same input within a single transaction; this lets PostgREST cache better and is the correct marker for a read-only similarity search.

### d) Grants and RLS for `anon`

```sql
GRANT USAGE ON SCHEMA reader TO anon;
GRANT SELECT ON public.readings TO anon;
GRANT SELECT ON reader.readings TO anon;
REVOKE EXECUTE ON FUNCTION reader.match_readings(...) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION reader.match_readings(...) TO anon;

ALTER TABLE readings ENABLE ROW LEVEL SECURITY;
CREATE POLICY readings_anon_select ON readings
    FOR SELECT TO anon
    USING (is_available = true);
```

The `is_available` column on `readings` defaults to `true`. Flip it to `false` on any row you want hidden from the Data API (moderation, quarantine, soft delete) without changing any code — the RLS policy will filter that row out of both `reader.readings` and `reader.match_readings` immediately.

The chain for a request:

1. PostgREST authenticates the request as `anon` (because the `apikey` is the publishable key).
2. `anon` needs `USAGE` on the `reader` schema to touch anything inside it.
3. The view runs `security_invoker = true`, so the underlying `SELECT` executes as `anon`, which has `SELECT` on `public.readings` + an RLS policy permitting rows where `is_available = true`.
4. The RPC has `GRANT EXECUTE` to `anon`. The function also filters `WHERE r.is_available = true` in its body, so it returns the same row set as the view — belt and suspenders.

Writes are silently blocked: no `INSERT`/`UPDATE`/`DELETE` grants on any target, and no matching RLS policies.

### What anon can actually see

| Concern | What gates it |
|---|---|
| Which rows of `readings` are visible | The RLS policy `USING (is_available = true)`. Rows with `is_available = false` are invisible to anon on both the view and the similarity RPC. |
| Which **columns** of `readings` are visible | The `reader.readings` view. It projects only `id, url, title, tldr`. Fields like `embedding`, `raw_output`, `summary`, `task_id`, `connections`, `is_available`, and `created_at` never leave the DB via the Data API. |
| Whether anon can write | RLS. No `INSERT`/`UPDATE`/`DELETE` policies exist on `readings`, so every write is rejected even if a `GRANT INSERT` were added later by mistake. |
| Whether anon can read other tables (`tasks`, `api_keys`, `instances`, …) | RLS. Migration 011 enables RLS on every existing table; none of them have an anon policy, so they're deny-all for anon. If `public` is ever added to the Data API's exposed schemas, those tables still return zero rows. |
| Whether anon can read `readings` with all columns | The combination of (a) exposing only the `reader` schema in the Data API, and (b) the `reader.readings` view projection. If `public` is ever exposed, anon's grants allow `SELECT *` on `public.readings` — RLS still filters by `is_available`, but every remaining column is visible. Don't expose `public`. |

RLS here does three jobs: **row-gating by `is_available`**, **blocking writes**, and **denying any access to sibling tables**. Column-level privacy on `readings` still comes from the view, not RLS.

## 5. Data API configuration

In the Supabase dashboard: **Project Settings → API → Data API Settings**.

### Exposed schemas

Add `reader` to the list. You can keep `public` too if anything else needs it, but the reader flow doesn't require `public` to be exposed.

The reader scripts send `Accept-Profile: reader` and `Content-Profile: reader` headers so PostgREST routes to the `reader` schema explicitly, independent of which schema is the default.

### Extra search path (optional)

Leave as-is. The function's `SET search_path` handles vector operator resolution; no project-wide search_path change is needed.

## 6. The four reader scripts

`docker/reader/` includes four short shell scripts that encapsulate the Data API calls. Each one fails fast with a clear error if its required env vars are missing.

| Script | Purpose | Env | Request |
|--------|---------|-----|---------|
| `read-embed.sh` | Embed text via OpenAI `text-embedding-3-small` | `OPENAI_API_KEY` | `POST api.openai.com/v1/embeddings` |
| `read-lookup.sh` | Exact-URL duplicate check | `SUPABASE_URL`, `SUPABASE_ANON_KEY` | `GET /rest/v1/readings?url=eq.<url>` with `Accept-Profile: reader` |
| `read-similar.sh` | Semantic search (embed + RPC) | `OPENAI_API_KEY`, `SUPABASE_URL`, `SUPABASE_ANON_KEY` | `POST /rest/v1/rpc/match_readings` with `Content-Profile: reader` |
| `reader-entrypoint.sh` | Image entrypoint: runs harness with the reading prompt, parses output, writes `status.json` | All of the above + `ANTHROPIC_API_KEY` or `OPENAI_API_KEY` for the harness | — |

The end-to-end smoke test `docker/reader/test_reader_e2e.sh` reads all required vars from `.env`, runs each script against the real services, and confirms the final entrypoint produces a `BACKFLOW_STATUS_JSON:` line with a populated `reading` object.

## 7. Complete `.env` additions

```bash
# Primary database (session pooler)
BACKFLOW_DATABASE_URL=postgresql://postgres.<ref>:<password>@aws-0-<region>.pooler.supabase.com:5432/postgres

# Reader Data API
SUPABASE_URL=https://<ref>.supabase.co
SUPABASE_ANON_KEY=sb_publishable_...
```

That's everything the reader needs from Supabase. There is intentionally no `SUPABASE_READER_KEY`, `SUPABASE_JWT_SECRET`, or signing key — those were dead ends (see section 3).

## 8. Troubleshooting

### `{"message":"Invalid API key"}` (HTTP 401)

Gateway-level rejection. The `apikey` header only accepts project-level publishable or secret keys, not custom JWTs. Put `sb_publishable_...` in `apikey`.

### `PGRST301: No suitable key or wrong key type` (HTTP 401)

PostgREST couldn't verify a JWT against the project JWKS. Usually because someone tried to mint a custom HS256 token. The fix is not to mint one — use the publishable key as documented.

### `{"code":"PGRST100", ...}` or HTTP 404 on `/rest/v1/rpc/match_readings`

PostgREST couldn't find the function in the current schema. Two causes:

- `reader` schema isn't in **Data API → Exposed schemas**. Add it.
- Request didn't include `Content-Profile: reader` (POST) or `Accept-Profile: reader` (GET). The reader scripts already send these; check your dashboard setting or any custom code.

### `operator does not exist: public.vector <=> public.vector`

The `match_readings` function was created without `SET search_path`. Migration 011 pins it correctly; if you authored a similar function yourself, add `SET search_path = reader, public, extensions` (or whichever schema pgvector is installed in).

### Reading table returns all columns via the Data API

You either hit `public.readings` instead of `reader.readings` (no `Accept-Profile` header, or `public` is exposed and the request doesn't specify a profile), or `reader` isn't in **Exposed schemas**. The intent is that only the four view columns are ever returned; if you see more, check both.

### Can't find the JWT private key in the dashboard

You're not supposed to. See section 3 — Supabase holds the private halves of its signing keys. Don't try to mint tokens.
