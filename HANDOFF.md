# HANDOFF.md

Ledger of cross-PR tradeoffs. Each entry: decision → consequence for downstream work.

## #175 — REST API reading task creation

- **`task_mode` allow-list: `""`, `"auto"`, `"read"`.** Explicit `"code"` / `"review"` rejected — prompt inference is still the only path. Loosen `CreateTaskRequest.Validate` if a future caller needs explicit opt-in.

## #174 — Reading completion pipeline

- **`Event.TaskMode` populated in `NewEvent`.** #177's Discord reading embed can branch on `event.TaskMode == "read"` without further plumbing. Reading fields (`TLDR`, `NoveltyVerdict`, `Tags`, `Connections`) are also already on the event for read-task completions.
- **Embeddings client is single-shot, no retries.** Failures surface as task failures; higher-level retry handles transients. Add retries later inside `OpenAIEmbedder` without changing the `Embedder` interface if 429/5xx rates become an issue.
- **`reading.raw_output` is the marshaled `AgentStatus`, not a separate agent field.** Lossless for typed fields; adding new agent output requires extending `AgentStatus` (two lines).
- **Deferred:** Discord reading embed formatting (#177), `GetReadingByURL`/reader accessors (no consumer yet), `SUPABASE_READER_KEY` custom JWT (dead-ended on Supabase side — using publishable key via `SUPABASE_ANON_KEY`, see `docs/supabase-setup.md`).
