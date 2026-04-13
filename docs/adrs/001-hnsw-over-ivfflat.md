# ADR-001: HNSW over IVFFlat for embedding index

## Status

Accepted

## Context

The `readings` table stores 1536-dimensional embeddings (OpenAI `text-embedding-3-small`) and needs a vector similarity index for `match_readings` queries. pgvector supports two ANN index types: IVFFlat and HNSW.

## Decision

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
