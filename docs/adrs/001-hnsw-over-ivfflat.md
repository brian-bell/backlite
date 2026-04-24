# ADR-001: Historical pgvector HNSW decision

## Status

Superseded by the SQLite migration

## Context

This ADR described the former Postgres + pgvector design for `readings` similarity search.
Backlite now stores embeddings as JSON text in SQLite and ranks cosine similarity in Go, so
the pgvector index decision is no longer part of the active architecture.

## Historical decision

Use HNSW (`CREATE INDEX ... USING hnsw (embedding vector_cosine_ops)`).

## Rationale

**IVFFlat builds centroids at `CREATE INDEX` time.** On an empty or small table the centroids are meaningless, so recall degrades until you `REINDEX` after seeding enough data. The rule of thumb is `lists ~ sqrt(rows)`, so the tuning parameter changes as the table grows. Each re-tuning requires a full reindex and a write lock.

**HNSW builds incrementally.** New rows are inserted into the graph on write, so the index is always usable regardless of table size. There is no equivalent of `lists` to re-tune.

Trade-offs accepted:

- HNSW indexes are larger on disk (~2-3x vs IVFFlat for the same dataset).
- HNSW index builds are slower than IVFFlat builds.
- Insert latency is slightly higher because each INSERT updates the graph.

At the expected scale (thousands to low tens-of-thousands of readings), these costs are negligible, and not needing a reindex workflow is worth it.

## Alternatives considered

- **IVFFlat with scheduled reindex**: Operationally complex, easy to forget, recall silently degrades between reindexes.
- **No index (exact scan)**: Fine for <1K rows but doesn't scale. Adding an index later requires a migration anyway, so better to start with one.
