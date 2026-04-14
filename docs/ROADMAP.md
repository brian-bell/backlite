# Backflow Productization Roadmap

## Context

Backflow is a self-hosted Go service that runs AI coding agents (Claude Code, Codex) in ephemeral containers. It's mature as a developer tool — REST API, Discord slash commands, SMS via Twilio, three infrastructure modes (EC2 spot, local Docker, ECS Fargate), cost tracking, spot interruption recovery, and a three-stage agent pipeline (Prep → Code/Review → Self-Review).

**The problem:** Backflow is a powerful internal tool but not yet a product. It lacks authentication, multi-tenancy, observability, scheduling, workflow orchestration, and several table-stakes reliability features. This plan transforms it from a developer utility into something teams and organizations would adopt, pay for, and recommend.

**Competitive landscape:** GitHub Copilot Workspace, Cursor background agents, Devin, Factory, SWE-agent, OpenHands, and Anthropic's own Claude Code GitHub Actions. Backflow's unique edges are infrastructure flexibility (EC2/local/Fargate), chat-first interfaces (Discord/SMS), multi-harness support (Claude + Codex), and cost optimization (spot instances + budget caps).

---

## Product Identity

**Tagline:** "Your team's coding agent infrastructure."

**Target persona:** Engineering leads and platform engineers at 10-200 person software companies who want agent-assisted development without handing repo access and API keys to third-party SaaS. They value cost predictability, infrastructure control, and chat-native interfaces.

**Secondary persona:** Individual developers dispatching agents from Discord or SMS to manage projects asynchronously.

---

## Tier 1: Fix the Foundation

The test harness (1.0) is the prerequisite for everything else. Once it exists, all subsequent features are developed TDD-style against the full running system — not just unit-tested against mocks. This is how we use Backflow to develop Backflow.

### 1.0 Black-Box Test Harness, Soak Testing, and OpenAPI Spec ✅
- **Status:** Implemented (see #99, #153, #154, #156, #157)
- **What shipped:** Black-box test harness (`test/blackbox/`) with fake agent image (`FAKE_OUTCOME`-parameterized), 7 outcome tests (happy path, agent failure, needs input, timeout, crash, cancellation, webhook resilience), programmable webhook listener, soak test (`test/soak/`) for memory/pool/container leak detection, `/debug/stats` endpoint, `max_runtime_sec` task field, `BACKFLOW_AGENT_IMAGE` config, OpenAPI spec (`api/openapi.yaml`), Schemathesis fuzz testing, CI scripts with dedicated blackbox job

---

### 1.1 Fix Discord Retry Race Condition ✅
- **Status:** Implemented (see #161)
- **What shipped:** `ready_for_retry` + `user_retry_count` columns, atomic `store.RetryTask`, `POST /tasks/{id}/retry` endpoint, `task.retry` event, configurable retry cap (`BACKFLOW_MAX_USER_RETRIES`, default 2), `TestCancelAndRetry` black-box test

### 1.2 API Authentication
- **Problem:** REST API has zero auth — anyone can create tasks and spend real money
- **Proposal:** Bearer-token middleware on `/api/v1/*`. New `api_keys` table (key_hash SHA-256, name, permissions, expires_at). Simple mode: `BACKFLOW_API_KEY` env var for single-key setups. Webhook endpoints and `/health` remain unauthenticated
- **Files:** `internal/api/server.go` (middleware), `internal/config/config.go`, `internal/store/` (new table), new migration
- **Metric:** 100% of API requests require auth when keys are configured; zero change for local-mode single-user
- **Harness validation:** Black-box test: unauthenticated request → 401; valid key → 200; expired key → 401
- **Status:** Implemented (see #165)
- **What shipped:** Bearer-token middleware on `/api/v1/*` and `/debug/stats`, `BACKFLOW_API_KEY` single-token mode, DB-backed `api_keys` table with SHA-256 lookup and scoped permissions, OpenAPI auth responses, black-box auth coverage

### 1.3 Slack Notification Integration
- **Problem:** Stubbed in `main.go` ("subscriber not yet implemented"). Many teams live in Slack, not Discord
- **Proposal:** `SlackNotifier` using incoming webhooks + Block Kit formatting. Thread-based grouping per task (use `ts` as thread parent). Follow existing `WebhookNotifier`/`DiscordNotifier` patterns. `BACKFLOW_SLACK_EVENTS` for filtering (config already exists)
- **Files:** `internal/notify/slack.go` (new), `cmd/backflow/main.go` (replace stub ~line 237)
- **Metric:** Slack messages delivered within 5s of event emission; threaded per task
- **Harness validation:** Unit tests with mock HTTP server verifying Block Kit payload format

### 1.4 Rate Limiting
- **Problem:** No protection against task queue flooding — each task costs real money
- **Proposal:** In-memory token bucket (`golang.org/x/time/rate`) middleware. `BACKFLOW_RATE_LIMIT_RPM` (default 60), `BACKFLOW_RATE_LIMIT_BURST` (default 10). Plus queue depth check: reject with 429 when pending tasks exceed `BACKFLOW_MAX_PENDING_TASKS`
- **Files:** `internal/api/server.go` (middleware), `internal/config/config.go`, `internal/api/handlers.go` (queue depth check)
- **Metric:** HTTP 429 on excess; no runaway task creation possible
- **Harness validation:** Black-box test: burst N+1 requests → Nth+1 returns 429

---

## Tier 2: Unlock Growth (2-4 weeks each)

### 2.1 Human-in-the-Loop Input Gate
- **Problem:** When the agent needs clarification, the task just fails. The `task.needs_input` event exists but is not actionable — users can't respond
- **Proposal:** New `needs_input` non-terminal status. Store agent's question in `pending_question` column. Users respond via:
  - API: `POST /tasks/{id}/respond`
  - Discord: reply in the task thread (detected via `discord_task_threads` lookup)
  - SMS: reply to the notification (matched by `reply_channel`)
  - Response creates a continuation task linked via `parent_task_id`
- **Files:** `internal/models/task.go`, `internal/orchestrator/monitor.go`, `internal/api/handlers.go` (new endpoint), `internal/discord/interactions.go`, `internal/store/`, new migration
- **Competitive edge:** Devin has chat-based iteration. Cursor agents can resume. No open-source runner has multi-channel input gates (API + Discord + SMS)
- **Metric:** >50% of `needs_input` events get user responses; reduced failure rate from input-blocked tasks

### 2.2 Observability Dashboard
- **Problem:** Only way to check health is `make db-running` and raw logs. No aggregate view of success rates, costs, or queue depth
- **Proposal:** Embedded HTML dashboard at `/dashboard` (Go `embed` package, vanilla JS + Chart.js). Plus `/api/v1/stats` JSON endpoint. Shows: task counts by status over time, cost by day/week, p50/p95 duration, success rate, queue depth, active instances
- **Files:** `internal/api/server.go`, `internal/api/dashboard.go` (new, embedded HTML), `internal/store/` (aggregate SQL queries)
- **Competitive edge:** No open-source agent runner has a built-in ops dashboard
- **Metric:** Answer "how is Backflow performing?" in <10 seconds

### 2.3 Task Scheduling and Recurring Tasks
- **Problem:** No cron jobs — users must build their own cron + API wrapper for nightly reviews or weekly dependency updates
- **Proposal:** New `schedules` table (id, name, cron_expression, task_template JSON, enabled, next_run_at). Scheduler goroutine alongside orchestrator poll loop. CRUD at `/api/v1/schedules`. `/backflow schedule` Discord command
- **Files:** `internal/orchestrator/scheduler.go` (new), `internal/store/` (schedule CRUD), `internal/api/` (routes), `internal/models/schedule.go` (new), new migration
- **Competitive edge:** Scheduling + Discord = "set up weekly PR reviews from your phone"
- **Metric:** Scheduled tasks fire within 60s of target time

### 2.4 Real-Time Log Streaming (SSE)
- **Problem:** Log access is polling-only. For 10-30min tasks, operators want live progress
- **Proposal:** SSE endpoint at `/api/v1/tasks/{id}/logs/stream`. Docker modes: `docker logs --follow` piped through SSE. Fargate: cursor-based `FilterLogEvents`. Auto-close on terminal status
- **Files:** `internal/api/handlers.go` (new handler), `internal/api/server.go`, `internal/orchestrator/fargate/fargate.go`, `internal/orchestrator/docker/docker.go`
- **Metric:** Log latency <3 seconds; zero missed lines

---

## Tier 3: Differentiate (4-8 weeks each)

### 3.1 Task Chaining and Workflow Orchestration
- **Problem:** Complex workflows (lint → implement → test → review) require manually chaining separate tasks
- **Proposal:** `workflows` table defining DAGs of task templates. Nodes trigger on `on_success`, `on_failure`, or `always`. Variable interpolation (`{{parent.pr_url}}`). Workflow CRUD at `/api/v1/workflows`. `/backflow workflow` Discord command
- **Files:** `internal/models/workflow.go` (new), `internal/store/`, `internal/orchestrator/workflow.go` (new), `internal/orchestrator/monitor.go` (trigger next steps), `internal/api/`, new migrations
- **Competitive edge:** No open-source agent runner has DAG orchestration. Combined with multi-harness, enables "Claude for code, Codex for review" pipelines

### 3.2 Intelligent Harness Routing
- **Problem:** Multi-harness is a dumb switch. No intelligence about which agent performs better for which task
- **Proposal:** Track success rate, median cost, and duration per harness/task-type/repo. Routing strategies: `fixed`, `cheapest`, `fastest`, `best`, `round_robin`. When harness is unspecified, auto-select based on historical performance
- **Files:** `internal/store/postgres.go` (aggregate queries), `internal/config/config.go`, `internal/api/task_creator.go` (routing logic)
- **Competitive edge:** Unique to Backflow — no competitor is multi-vendor, let alone intelligently routed

### 3.3 Quality Gates: Pre-PR Validation
- **Problem:** Agent PRs sometimes fail CI because the agent didn't run tests or lint
- **Proposal:** Configurable `quality_gates` field (list of shell commands). Run after code changes, before PR creation. On gate failure, agent gets error output and retries once. PR description includes gate results. Also read gates from `.backflow.yml` in repo
- **Files:** `docker/agent/entrypoint.sh` (new step between code and PR), `internal/models/task.go`, `internal/config/config.go`
- **Competitive edge:** More flexible than competitors — user-defined shell commands, not hard-coded test runners

### 3.4 Evaluation and Metrics Framework
- **Problem:** No way to measure agent quality. Can't make data-driven decisions about model selection or prompt engineering
- **Proposal:** `task_evaluations` table tracking: tests passed, PR merged (poll GitHub), time to merge, revision count, human rating (1-5). `/backflow rate <task_id> <1-5>` Discord command. Surfaced in dashboard
- **Files:** `internal/store/` (new table), `internal/orchestrator/evaluator.go` (new background job), `internal/discord/interactions.go`, `internal/api/`

---

## Tier 4: Platform Play (2-3 months each)

### 4.1 Multi-Tenancy and Team Management
- **Proposal:** `teams` table, `team_id` FK on tasks/api_keys/schedules. Per-team budgets, harness prefs, notification channels. Discord guild = team. Team-scoped queries. Unlocks paid tier
- **Business impact:** Required for org adoption and SaaS pricing

### 4.2 Repository Config Files (`.backflow.yml`)
- **Proposal:** Repo-level config for quality gates, allowed tools, CLAUDE.md, budget caps, review guidelines. Merged with task > repo > global priority
- **Business impact:** Makes repos self-describing for agent use; reduces per-task configuration friction

### 4.3 Plugin / Harness SDK
- **Proposal:** Standardized harness contract: Docker image + env vars in → `status.json` out. Harness registry (DB or config). Third parties register harnesses via API without modifying Go code
- **Business impact:** Transforms Backflow from "agent runner" to "agent platform"

### 4.4 Cross-Repo and Monorepo Support
- **Proposal:** `working_directory` field for monorepo subdirectory targeting. `repos` list for multi-repo checkouts. Cross-repo changes via workflow steps
- **Business impact:** Removes adoption blocker for large engineering orgs

---

## Future Directions (Not Committed)

### Fly Machines as an agent runtime
- **Idea:** Add a `fly` operating mode alongside `ec2` / `local` / `fargate`. One Fly Machine per Backflow task, provisioned via the Machines API, mirroring the Fargate adapter's lifecycle.
- **Appeal:** Consolidation (the orchestrator already runs on Fly, so agents there = one cloud, no ECS cluster / task def / VPC / IAM roles to maintain). Cold starts ~1s vs Fargate's 30–60s ENI attachment. Simpler setup, per-second billing, more regions.
- **Against:** No spot tier (Fargate Spot is ~70% cheaper for bursty batch workloads). S3 stays a dependency unless we also migrate agent output + large-prompt offload to Tigris/R2. The existing `internal/orchestrator/fargate/` adapter (log parsing, CloudWatch, recovery) is real sunk work to replicate. Fargate has a higher resource ceiling and more mature observability.
- **Revisit when:** (a) we want to drop AWS entirely and are ready to migrate storage, or (b) cold-start latency becomes a real UX complaint. Until one of those triggers, Fargate for agents + Fly for the orchestrator is the right split.

---

## Business Model: Open-Core + Managed Hosting

| Tier | What's Included | Price |
|------|----------------|-------|
| **Open Source** (free) | Core orchestrator (all 3 modes), all harnesses, REST API, Discord/SMS/Slack, single-tenant, basic dashboard | Free forever |
| **Backflow Pro** (self-hosted license) | Multi-tenancy, workflow DAGs, intelligent routing, evaluation framework, scheduling, advanced analytics, SSO/SAML, priority support | $99/mo per team (up to 10 users) |
| **Backflow Cloud** (managed SaaS) | Fully managed hosting, zero ops, usage-based, auto-scaling, audit logging, compliance | $0.10/agent-minute or $0.50-2.00/task |

---

## Implementation Sequence

| Phase | Focus | Items | Dependency |
|-------|-------|-------|------------|
| **Week 1-2** | Test harness prod changes | 1.0a: configurable agent image, max_runtime_sec, /debug/stats | None |
| **Week 2-3** | Fake agent + harness infra | 1.0b: fake agent image, 1.0c: TestMain, webhook listener, test client | 1.0a |
| **Week 3-4** | Black-box tests + OpenAPI | 1.0d: 7 tests, 1.0e: OpenAPI spec + kin-openapi validation | 1.0c |
| **Week 4-5** | Soak + CI | 1.0f: soak test, 1.0g: scripts + CI integration | 1.0d |
| **Week 5-8** | Hardening (parallel) | 1.1 retry fix, 1.2 API auth, 1.3 Slack, 1.4 rate limiting | 1.0d (each adds black-box tests) |
| **Month 3-4** | Growth | Tier 2: dashboard, human-in-the-loop, scheduling, log streaming | Tier 1 complete |
| **Month 5-8** | Differentiation | Tier 3: quality gates, workflows, harness routing, eval framework | Tier 2 |
| **Month 9-14** | Platform | Tier 4: multi-tenancy, repo config, plugin SDK, cross-repo | Tier 3 |

The key insight: items 1.1-1.4 are developed **against the black-box harness**. Each one adds new black-box tests alongside its implementation, catching regressions across the full system.

---

## Critical Files (Implementation Entry Points)

- `internal/api/server.go` — Central router; all new endpoints, middleware (auth, rate limiting), and routes mount here
- `internal/store/store.go` — Store interface; every new table and feature must extend this contract
- `internal/orchestrator/monitor.go` — Completion handling; human-in-the-loop, quality gates, workflow triggers, and eval hooks integrate here
- `internal/orchestrator/orchestrator.go:33,64-73,111-143,217-230` — Running counter (for /debug/stats) and `initLocalMode`/`syncSyntheticInstance` (for test harness setup)
- `internal/orchestrator/docker/docker.go:93` — `buildRunCommand()` uses `config.AgentImage`
- `internal/orchestrator/ec2/scaler.go:78` — `isDockerReady()` uses `config.AgentImage`
- `internal/config/config.go` — All env vars and defaults (AgentImage, MaxRuntimeSec, etc.)
- `internal/config/defaults.go:12,35` — TaskDefaults struct (MaxRuntimeSec)
- `internal/models/task.go:54,119,138` — Task struct, CreateTaskRequest, validation
- `internal/notify/webhook.go` — Notification pattern that Slack and future notifiers must follow
- `docker/agent/entrypoint.sh` — Agent pipeline; quality gates, repo config parsing, and harness SDK dispatching live here
- `cmd/backflow/main.go` — Service wiring; new goroutines (scheduler, evaluator) start here
- `scripts/create-task.sh`, `scripts/review-pr.sh` — API clients using `max_runtime_sec`

---

## Verification

### For the test harness itself (1.0)
1. `make test` — all existing tests still pass after prod code changes (1.0a)
2. `make lint` + `gofmt` — no formatting issues
3. `docker build test/blackbox/fake-agent/` succeeds and all 5 FAKE_OUTCOME modes work
4. `make test-blackbox` — all 7 black-box tests pass (happy path, failure, needs_input, timeout, crash, cancellation, webhook resilience)
5. Every HTTP response in the harness is validated against `api/openapi.yaml` by kin-openapi — zero spec violations
6. `make test-soak --short` completes a 10-minute run with all metrics within thresholds
7. `make test-schema` — schemathesis finds no crashes or 500s from fuzzed requests

### For subsequent features (1.1-1.4 and beyond)
1. `make test` — unit tests pass
2. `make test-blackbox` — black-box tests pass (including new tests added for the feature)
3. `make lint` — no warnings
4. New features developed TDD: write failing black-box test first, then implement
5. Each feature's "Harness validation" bullet describes the specific black-box test to add
