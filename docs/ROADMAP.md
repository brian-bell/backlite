# Backflow Roadmap

## Context

Backflow is a self-hosted Go service that runs AI coding agents (Claude Code, Codex) in ephemeral containers. It supports three operating modes (EC2 spot, local Docker, ECS Fargate), three task modes (`code`, `review`, `read`), API and SMS interfaces, cost tracking, and a fake-agent test harness for end-to-end testing.

This roadmap reframes Backflow around its actual user: a solo developer who wants Backflow to (a) be useful daily, and (b) serve as a strong portfolio/showcase project. Earlier drafts targeted "engineering leads at 10-200 person companies" with multi-tenancy, SSO, and a paid tier — work that doesn't match the user, the audience, or the time budget. That framing has been retired.

---

## Product Identity

**One-line description:** A self-hostable agent runner that ships PRs, reviews PRs, and reads URLs into a personal knowledge base — controlled from your terminal or your phone.

**Primary user:** The author. Daily-use test: does this save >15 min/day? Do I open it without forcing myself?

**Secondary audience:** Engineers evaluating the codebase as a portfolio artifact. Portfolio test: can a staff engineer skim the README + a couple files in 10 min and conclude "this person ships real systems"? Is there a 30-second wow?

## Business Model

Open source. Self-host. If managed hosting becomes interesting later, that's a future conversation. No tiers, no Pro license, no SaaS pricing on the roadmap today.

---

## What's Already Shipped

### Black-Box Test Harness, Soak Testing, OpenAPI Spec ✅
See #99, #153, #154, #156, #157. Black-box harness in `test/blackbox/` with parameterized fake agent, 7 outcome tests, programmable webhook listener, soak test, `/debug/stats`, `max_runtime_sec`, OpenAPI spec, Schemathesis fuzz testing.

### Retry Flow Fix ✅
See #161. Atomic `RetryTask`, `POST /tasks/{id}/retry`, `task.retry` event, configurable retry cap, black-box coverage.

### API Authentication ✅
See #165. Bearer-token middleware on `/api/v1/*` and `/debug/stats`, single-token `BACKFLOW_API_KEY` mode, DB-backed `api_keys` table with SHA-256 lookup and scoped permissions.

### Reading Mode ✅
See #174, #181, #185. New `read` task mode that fetches a URL, summarizes it via a dedicated reader agent image, embeds the TLDR via OpenAI, and stores the result in the `readings` table for similarity search. Per-task `agent_image` field, reader Docker image, `Force` flag for re-summarizing.

---

## Active Roadmap

Four items, ordered for execution. The previously-planned standalone dashboard has been merged into item #1.

### 1. Public Site — Landing + Live Dashboard

A new front-end project that serves two purposes from one codebase:

- **Marketing surface**: hero, 60-second demo video, "what's interesting about this codebase" callouts (fake-agent test harness, three-mode orchestrator, multi-harness support, acceptance-review agent team).
- **Operator surface**: dashboard previously specced as a separate roadmap item — task counts by status over time, cost by day/week, p50/p95 duration, success rate, queue depth, active instances, plus a reading inbox view (see item #3).

The site is publicly accessible. Visitors see a **seeded demo database** with sanitized historical tasks and readings — they can poke around real-looking data without deploying Backflow. Authenticated operator (the user) gets the same surfaces against the live database.

**Why this is item #1:** without a public surface, every other piece of work has no audience. Items #2-#4 need something to link to.

**Files:** new top-level directory (e.g. `site/`); no changes required to `internal/api/server.go` if the site deploys separately. If it deploys as part of the same Fly app, mount under `/` and move the API to `/api/v1/`.

**Open questions to resolve before starting:**
- SPA framework choice (React/Vite, SvelteKit, Astro, or Go templates + HTMX)
- Deploy target (same Fly app as the API, or separate Fly app / Cloudflare Pages)
- Demo data source (separate Supabase project, separate schema, or static JSON snapshot baked into the build)
- Operator auth model (magic link, API key in localStorage, or Cloudflare Access policy)

**Effort:** M-L. **Validation:** site renders demo data publicly; operator view loads against live DB; demo video plays; portfolio callouts link to the right files.

---

### 2. Reading Mode Investments

Doubles down on the most novel feature in the codebase.

#### 2.1 Reading Inbox UI
Lives inside the site (item #1) as a second top-level surface alongside the dashboard. Search past readings (full-text + similarity), browse by tag, view the connection graph from `connections` JSONB, click into a reading to view raw output and re-summarize.

**Files:** site (item #1); new endpoints under `internal/api/handlers.go` for `/api/v1/readings` (list, get); `internal/store/postgres.go` for query helpers (`ListReadings`, `SearchReadings`).

#### 2.2 Daily Digest
Once-a-day SMS digest with newly-read items + "you might want to revisit X" suggestions based on novelty scores and connection density. No new UI.

**Files:** new `internal/orchestrator/digest.go` goroutine alongside the poll loop; uses existing `notify.EventBus` or sends directly via `MessagingNotifier`.

**Why this matters:** the "SMS a URL → TLDR back → connections to prior reads" loop has no comparable open-source product. Code-agent runners are commodity; chat-native personal-knowledge agents are not. This is the differentiator both for daily use (you'll actually return to past readings) and for portfolio narrative (the genuinely-unique thing in this codebase).

**Effort:** M (2.1) + S (2.2). **Validation:** can find a reading from 30 days ago via search; daily digest fires at the configured time and includes both new readings and revisit suggestions.

---

### 3. Dashboard — Merged Into Item #1

No longer a standalone roadmap item. Built as the operator surface inside the public site.

**Implementation note:** the merge implies the site needs a lightweight operator-vs-visitor auth concept and an environment switch between demo and production data sources from day one — design these into item #1 rather than retrofitting.

---

### 4. Distribution Post

One serious blog post submitted to HN and lobste.rs, published once items #1 and #2 are live so the post links to a working demo. Working title: *"Building a multi-mode coding agent runner in Go."*

Substantive technical content drawn from real decisions:
- Why polling beats events for this workload
- The fake-agent + `FAKE_OUTCOME` parameterized test pattern
- EC2 spot vs Fargate vs local — when to choose each
- Re-embedding the agent's TLDR rather than trusting it (the actual reading-mode design decision)

**Why last:** distribution before substance burns the one shot you get on those channels. Once the site has a live demo and the reading inbox shows real value, the post has something to point at.

**Effort:** M. **Validation:** post reaches HN or lobste.rs front page; site analytics show inbound traffic; GitHub stars trend up.

---

## What Was Dropped

The following items from prior roadmap drafts are explicitly NOT on the active plan. Each was dropped because it served the wrong product (multi-user team) or had XL effort with single-user payoff.

| Dropped item | Reason |
|--------------|--------|
| 1.3 Slack Notifier | SMS already covers the away-from-keyboard use case; Slack is "team" thinking |
| 1.4 Rate Limiting | Reframe later as a budget circuit breaker if a runaway-spend incident actually happens |
| 2.1 Human-in-the-Loop Input Gate | Cut at user direction; `task.needs_input` event remains as-is but no response pathway will be built |
| 2.3 Scheduling | Cron + curl covers it; no recurring jobs needed today |
| 2.4 Real-Time Log Streaming | Polling logs is fine for one user |
| 3.1 Workflow DAGs | XL effort, single-user payoff |
| 3.2 Intelligent Harness Routing | Premature optimization; pick the harness manually |
| 3.3 Quality Gates | Claude Code runs your gates if you tell it to in the prompt |
| 3.4 Evaluation Framework | No team to vote, no hiring decision to make |
| 4.1 Multi-Tenancy | Wrong product |
| 4.2 Repository Config Files (`.backflow.yml`) | Marginal value for one user with one config |
| 4.3 Plugin / Harness SDK | Fantasy adoption assumption |
| 4.4 Cross-Repo / Monorepo Support | No real workflow needs this today |
| Open Source / Pro / Cloud business model | No PMF, no sales channel, no time |
| Fly Machines as agent runtime | Future direction; revisit only if dropping AWS or hitting cold-start UX complaints |

---

## Implementation Sequence

| Phase | Item | Notes |
|-------|------|-------|
| **Now** | Site scaffolding (#1) — framework choice, deploy story, design system, marketing surface | Decide SPA vs SSR up front; dashboard needs interactive charts |
| **Next** | Demo data seeding + operator dashboard surface (#1 continued) | Wire the demo-vs-live data switch |
| **Then** | Reading inbox UI (#2.1) | Second top-level surface in the site |
| **Then** | Daily digest (#2.2) | Backend cron, no UI |
| **Then** | 60-second demo video | Recorded against demo or real data, embedded in landing |
| **Last** | HN/lobste.rs post (#4) | Links to live demo and inbox |

Estimated total: **6-10 weeks** of focused evening/weekend work.

---

## Critical Files (Implementation Entry Points)

- `internal/api/server.go` — Router; reading endpoints mount here
- `internal/store/store.go` — Store interface; reading list/search helpers extend it
- `internal/orchestrator/monitor.go` — `handleReadingCompletion` already exists; digest goroutine is a sibling
- `internal/orchestrator/orchestrator.go` — Site auth and demo-data switch will likely touch startup wiring
- `internal/notify/` — `MessagingNotifier` for the daily digest delivery
- `cmd/backflow/main.go` — New goroutines (digest) start here
- `docker/reader/` — Reader image; reading-mode investments may extend the script
- `site/` — New top-level directory for the public site (item #1)

---

## Verification

Each item's validation criterion lives inline above. Cross-cutting requirements:

1. `make test` and `make test-blackbox` pass after every change
2. `make lint` clean
3. New API endpoints have black-box tests in `test/blackbox/`
4. Site changes are validated by loading the dev server and using the feature in a browser before declaring done
5. `HANDOFF.md` updated with any cross-PR tradeoffs introduced
