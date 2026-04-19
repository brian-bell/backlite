Now I'll synthesize all five reports into the final acceptance review.

---

# Feature Acceptance Review: Fargate Orchestration Mode

## Summary

The Fargate feature runs each Backflow task as a standalone ECS/Fargate task, implementing the `Runner` interface against ECS and CloudWatch. It is Backflow's production deployment mode, making its correctness foundational to every roadmap item. The happy path — task dispatch, status monitoring, spot interruption recovery, and large env-var offload to S3 — is well-implemented and running in production. However, two correctness bugs cause ECS failure scenarios to be silently misreported as "completed," and the documentation has multiple significant gaps that will block first-time operators. Five reviewers identified two blockers, eight significant issues, and eleven minor/note findings.

## Verdict: REQUEST CHANGES

### Blockers

**1. ECS container-start failure silently reported as `completed`**
`handleCompletion` in `monitor.go` determines success via `status.Complete || (status.ExitCode == 0 && !status.NeedsInput)`. When an ECS task fails before its container starts (image pull error, IAM permission denied, resource constraints, bad task definition), there is no container exit code (`ExitCode` stays `0`), no `BACKFLOW_STATUS_JSON` line in CloudWatch (`Complete` stays `false`, `NeedsInput` stays `false`). The branch evaluates to `true` and the task is marked **completed** — not failed — with no PR URL, no error message, and no diagnostic signal. This is the most common ECS failure mode and it is happening in production today. Users cannot distinguish "agent succeeded but produced no PR" from "ECS never ran the container." Fix: treat `(ExitCode == 0 && !status.Complete && status.Error != "")` as a failure path in `handleCompletion`, or add a `FailedToStart` sentinel to `ContainerStatus`.

**2. `ensureClients` data race between orchestrator and API goroutines**
`m.ecs` and `m.cwLogs` in `fargate.go:39–56` are read and conditionally written without a mutex. The orchestrator poll loop calls `RunAgent`/`InspectContainer`/`StopContainer` (all calling `ensureClients`) from one goroutine; the API `/tasks/{id}/logs` handler calls `GetLogs` (also calling `ensureClients`) from another. This is a real data race under Go's memory model — the race detector will flag it. Currently compensated by `BACKFLOW_RESTRICT_API=true` blocking the logs endpoint in production, but when Roadmap 1.2 (API authentication) lifts that restriction, the race becomes exploitable. Fix: initialize clients with `sync.Once`.

---

### Significant Issues

**1. CloudWatch delivery race causes lost status JSON on completed tasks**
`InspectContainer` fetches CloudWatch logs in the same poll cycle that ECS first reports `STOPPED`. CloudWatch log delivery is eventually consistent and can lag task termination, especially for fast-running tasks or under cluster load. If `parseStatusFromLogEvents` finds no events, `handleCompletion` runs with `Complete=false`, `ExitCode=0`, `NeedsInput=false` — same misreport path as Blocker 1, but with a race timing root cause. Since the task immediately transitions to a terminal status, no retry occurs. Consider treating "task is STOPPED but CloudWatch returned zero events" as non-terminal for 1–2 additional poll cycles before finalizing.

**2. No AWS-mocked integration tests for core I/O paths**
`RunAgent`, `InspectContainer`, `StopContainer`, `GetLogs`, `GetAgentOutput`, and the S3 offload path all interact with AWS but have zero test coverage. No mock interfaces exist for `ecs.Client` or `cloudwatchlogs.Client`. Any regression in API call shape, field mapping, or ARN parsing is invisible until production. The `Runner` interface compile-time check in the test file is good hygiene, but the core production paths have no regression protection. This is the most pressing structural gap — especially since Roadmap 1.0 (black-box test harness) only targets local/Docker mode.

**3. Credentials passed as plaintext in ECS task override payload**
`buildECSEnvVars` passes `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, and `GITHUB_TOKEN` as plaintext ECS container environment variables via `RunTask` `ContainerOverrides`. This means they appear in AWS CloudTrail logs for every `RunTask` call, in `DescribeTasks` API responses (readable by any principal with `ecs:DescribeTasks` on the cluster), and at the ECS Task Metadata Endpoint from within the container. The ECS-native alternative — `secretsFrom` referencing Secrets Manager or Parameter Store ARNs in the base task definition — keeps credential values entirely out of the override plane. This should be addressed before Roadmap 1.2 broadens the task-creation surface.

**4. Offloaded S3 task data is never deleted**
`offloadLargeEnvVars` uploads PROMPT, CLAUDE_MD, TASK_CONTEXT, and PR_BODY to `task-config/{taskID}/{field}` in S3 but neither the fargate package nor the orchestrator completion path ever deletes these objects. Task prompts and injected CLAUDE.md content (which may contain sensitive project context) accumulate in the bucket indefinitely. The `S3Client` interface has no `Delete` method. Add cleanup in the task completion path when `status.Done == true`.

**5. `task.EnvVars` can shadow system credentials before Roadmap 1.2**
`buildECSEnvVars` appends user-supplied `task.EnvVars` after system credentials. ECS (and the Linux container runtime) resolves duplicate keys in favor of the last occurrence, so a user submitting `env_vars: {"ANTHROPIC_API_KEY": "attacker-key"}` can redirect the AI spend. In the current production configuration (Discord-only, role-gated), this is low risk. But a guard stripping reserved key names from `task.EnvVars` must be in place before Roadmap 1.2 reopens the REST API.

**6. `GetLogs` interface incompatible with Roadmap 2.4 (SSE streaming)**
The `Runner` interface's `GetLogs(ctx, instanceID, containerID string, tail int) (string, error)` returns a batch string snapshot. Roadmap Tier 2.4 (Real-Time Log Streaming) explicitly requires "Fargate: cursor-based `FilterLogEvents`" — a different CloudWatch API with pagination and an entirely different call pattern. Implementing SSE will require either a new interface method or a breaking change to `Runner`. This should be planned before Tier 2.4 work begins so the refactor doesn't surprise downstream implementations (DockerManager also implements `Runner`).

**7. Startup counter reset enables over-dispatch on rolling deploy**
`syncSyntheticInstance` calls `ResetRunningContainers` on every start when the fargate instance already exists (`orchestrator.go:140`). A rolling deploy — even a brief overlap of two Backflow instances — causes both instances to reset the counter to 0 and each dispatches up to `MaxConcurrentTasks`, potentially running 2× the intended concurrency simultaneously. Document the single-instance assumption explicitly in the ops runbook; or gate the counter reset on a leader-election / startup lock.

**8. Documentation has four significant gaps**
_(a)_ CLAUDE.md's Fargate section lists explicit defaults for 5 env vars, directly violating the project's own "do not record default values" guideline from the Documentation guidelines section of the same file.
_(b)_ The S3 env-var offload feature — which silently activates on prompts exceeding ~7 KB, requires `BACKFLOW_S3_BUCKET`, and needs specific IAM permissions (`s3:PutObject`, `s3:GetObject` on `task-config/*`) — is completely undocumented.
_(c)_ `BACKFLOW_CONTAINER_CPUS` and `BACKFLOW_CONTAINER_MEMORY_GB` directly size the ECS task (CPU units and memory MiB) and are absent from the CLAUDE.md Fargate section. An operator leaving them at defaults would not know what compute their agents are getting.
_(d)_ There is no `docs/fargate-setup.md`. Discord and SMS each have dedicated setup guides (96 and 74 lines respectively) covering step-by-step configuration, prerequisites, and how to verify the integration. Fargate is the production deployment mode and has none, leaving operators without actionable IAM policy examples, CloudWatch log group setup instructions, or ECS cluster/task-definition guidance.

---

### Minor Suggestions

1. **STOPPING/DEPROVISIONING treated as `Done` prematurely** — `mapECSTaskStatus` marks these intermediate states as `Done: true`, triggering log fetch and status parsing before the agent has finished writing. Consider treating only `STOPPED` as truly terminal.

2. **FARGATE_SPOT has no on-demand fallback** — FARGATE capacity provider weight is set to 0. If Spot capacity is unavailable, task placement fails entirely. An optional `BACKFLOW_ECS_SPOT_FALLBACK=true` config flag could enable on-demand fallback for SLA-sensitive deployments.

3. **Silent CloudWatch fetch failure logged at Debug** — When `getLogEvents` fails for a completed task (e.g., log stream name mismatch from a misconfigured `awslogs-stream-prefix`), `InspectContainer` logs at Debug and returns a status with no PR URL, no cost, and no completion flag. Operators have no signal. Promote to Warn for completed tasks.

4. **`ContainerCPUs` not validated against Fargate's valid CPU values** — ECS rejects values that aren't powers-of-two vCPU multiples (256/512/1024/2048/4096/8192/16384 units). `BACKFLOW_CONTAINER_CPUS=3` would submit 3072 CPU units and fail with a cryptic ECS error. Add a Fargate-mode validation against the known-valid set.

5. **S3 presigned URL 1-hour TTL** — If an ECS task queues for >1 hour before starting (FARGATE_SPOT capacity crunch), the agent gets a 403 fetching its config. Tie TTL to `max(MaxRuntimeMin, 60min) + buffer`.

6. **Spot interruption string matching is fragile** — `isSpotInterruptionReason` matches on AWS-controlled reason strings that could change without versioning. Add a comment citing the AWS source and add a test that at minimum documents the expected strings.

7. **`MaxConcurrentTasks` has no upper-bound guard** — Only validated `>= 1`. A misconfigured value of 1000 would exhaust ECS account limits and incur runaway cost.

8. **`ECSAssignPublicIP` defaults to true** — Document that `BACKFLOW_ECS_ASSIGN_PUBLIC_IP=false` is preferred when subnets have NAT gateway egress, and recommend outbound-443-only security groups.

9. **BACKFLOW_STATUS_JSON protocol undocumented** — Operators debugging a stuck task need to know the line format, the JSON schema fields, and that the orchestrator scans from the tail of the last 200 log lines. Add a brief mention in the Fargate section or operator runbook.

10. **CloudWatch log stream naming convention undocumented** — The format `{prefix}/{container}/{taskID-from-ARN}` is required to manually find logs in the AWS console. Document it in the Fargate section or a setup guide.

11. **DB counter drift after double restart with recovering tasks** — `recoverOnStartup` doesn't re-increment the per-instance container counter for tasks in `recovering` state, so after a second restart with recovering Fargate tasks the counter drifts negative on completion.

---

### Notes

- **ClientToken (ULID) is safe** — 80 bits of random entropy with `bf_` prefix; no collision or prediction risk. Correctly implemented.
- **API lockout is an effective compensating control for now** — `BACKFLOW_RESTRICT_API=true` substantially limits the current attack surface. Roadmap 1.2 must ensure bearer-token auth middleware is in place *before* this restriction is lifted.
- **Multi-tenancy (Roadmap 4.1) will require synthetic instance redesign** — Per-team concurrency limits would need one synthetic instance per team and team-aware dispatch filtering. The current single-row design is a known single-tenant constraint.
- **Roadmap 2.4 SSE design should begin now** — The Fargate-specific implementation is the most work. Early design will avoid a breaking `Runner` interface change mid-roadmap.

---

## Reviewer Reports

### Product
The feature correctly implements the `Runner` interface for ECS and is production-functional for the happy path. Key forward-looking gaps: the `GetLogEvents` vs. `FilterLogEvents` distinction will require significant rework for Roadmap 2.4 SSE; the async nature of `StopTask` compounds the known Discord retry race (Roadmap 1.1); and `BACKFLOW_RESTRICT_API=true` means the production instance is currently Discord-only, with Roadmap 1.2 needed to make the feature accessible to the primary API persona.

### Security
Fargate is safe to operate in the current `BACKFLOW_RESTRICT_API=true` / Discord-only production configuration. The two findings that must be addressed before Roadmap 1.2 reopens the REST API: plaintext credentials in the ECS task override plane (should use Secrets Manager `secretsFrom` references), and unbounded S3 accumulation of task content. The `task.EnvVars` credential shadowing vector also needs a key-allow-list guard before API auth ships.

### Quality
The pure-logic helpers are well-tested (status mapping, log parsing, env var building, spot interruption detection). The serious gaps are: container-start failure silently marked completed (most common ECS failure mode); CloudWatch delivery race causing lost status metadata on fast/failing tasks; and zero AWS-mocked integration tests for `RunAgent`, `InspectContainer`, `StopContainer`, and `GetLogs`. Without mock interfaces for `ecs.Client` and `cloudwatchlogs.Client`, the production I/O paths have no regression protection.

### Maintainability
The `ensureClients` data race is the feature's only true structural blocker — it's a real Go memory model violation between the API and orchestrator goroutines that `sync.Once` resolves cleanly. Secondary concerns: the silent Debug-level log on CloudWatch lookup failure makes misconfiguration nearly invisible in production; the `ecsOverridesTarget` constant has no comment explaining the 8192-byte ECS limit relationship; and the `GetLogs` interface must be evolved before Roadmap 2.4 to avoid a breaking change mid-implementation. The synthetic instance pattern works cleanly for single-instance deployments but encodes an assumption that should be documented.

### Documentation
The most urgent gap is the self-contradicting default values in CLAUDE.md's Fargate section — the docs violate a rule written in the same file. The most dangerous gap is the S3 offload feature: operators setting up Fargate for the first time will not know `BACKFLOW_S3_BUCKET` is required for long prompts, what IAM permissions the task role needs for S3, or how to diagnose "where did my prompt go?" failures. A `docs/fargate-setup.md` modeled on `discord-setup.md` — covering ECS cluster setup, task definition requirements, CloudWatch log group creation, and concrete IAM policy examples — would address most gaps in one document.