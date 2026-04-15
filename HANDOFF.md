# HANDOFF.md

Ledger of cross-PR tradeoffs. Each entry: decision → consequence for downstream work.

## #175 — REST API reading task creation

- **`task_mode` allow-list: `""`, `"auto"`, `"read"`.** Explicit `"code"` / `"review"` rejected — prompt inference is still the only path. Loosen `CreateTaskRequest.Validate` if a future caller needs explicit opt-in.
- **Read dispatch lives in `api.NewTask`, not the handler.** Discord modal (#176), SMS, and any future caller inherit read-mode support as soon as they pass `task_mode` through.
- **`FORCE` reserved and always emitted.** Docker and Fargate pass `FORCE=%t` for every task; non-read tasks get `FORCE=false` (agent ignores).

## #174 — Reading completion pipeline

- **`Task.Force` added here, not #175.** `models.Task` gains `Force bool` + migration `012_task_force.sql`. #175 is now pure API wiring — add `Force *bool` to `CreateTaskRequest` and copy it in at creation time, no schema work.
- **`Event.TaskMode` populated in `NewEvent`.** One-line change. #177's Discord reading embed can branch on `event.TaskMode == "read"` without further plumbing. Reading fields (`TLDR`, `NoveltyVerdict`, `Tags`, `Connections`) are also already on the event for read-task completions.
- **Embeddings client is single-shot, no retries.** Failures surface as task failures; higher-level retry handles transients. Add retries later inside `OpenAIEmbedder` without changing the `Embedder` interface if 429/5xx rates become an issue.
- **`reading.raw_output` is the marshaled `AgentStatus`, not a separate agent field.** Lossless for typed fields; adding new agent output requires extending `AgentStatus` (two lines).
- **Deferred:** Discord reading embed formatting (#177), `GetReadingByURL`/reader accessors (no consumer yet), `SUPABASE_READER_KEY` custom JWT (dead-ended on Supabase side — using publishable key via `SUPABASE_ANON_KEY`, see `docs/supabase-setup.md`).
