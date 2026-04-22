# Supabase Setup (Deprecated)

Backflow no longer uses Supabase or Postgres.

The current runtime stores all application data in a local SQLite database at `BACKFLOW_DATABASE_PATH`, and reader containers query duplicate/similarity data through Backflow's own HTTP API instead of PostgREST.

If you are configuring a new deployment:

```bash
BACKFLOW_DATABASE_PATH=./backflow.db
```

Optional reader override:

```bash
BACKFLOW_INTERNAL_API_BASE_URL=http://host.docker.internal:8080
```

This file remains only as a pointer for older notes and links.
