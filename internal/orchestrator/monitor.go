package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/orchestrator/chain"
	"github.com/brian-bell/backlite/internal/orchestrator/lifecycle"
	"github.com/brian-bell/backlite/internal/store"
)

// monitorCancelled cleans up tasks that were cancelled via the API while they
// were running or recovering. These tasks still have a container that needs
// stopping and were counted in o.running, which needs decrementing.
func (o *Orchestrator) monitorCancelled(ctx context.Context) {
	cancelled := models.TaskStatusCancelled
	tasks, err := o.store.ListTasks(ctx, store.TaskFilter{Status: &cancelled})
	if err != nil {
		log.Error().Err(err).Msg("failed to list cancelled tasks")
		return
	}

	for _, task := range tasks {
		if task.ContainerID == "" {
			// No container to clean up (cancelled while pending/provisioning).
			if !task.ReadyForRetry {
				o.markRetryReady(ctx, task, notify.EventTaskCancelled)
			}
			continue
		}

		if err := o.docker.StopContainer(ctx, task.ContainerID); err != nil {
			log.Warn().Err(err).Str("task_id", task.ID).Msg("monitorCancelled: failed to stop container")
		}
		o.releaseSlot(ctx, task)

		// Clear assignment so we don't process this task again
		if err := o.store.ClearTaskAssignment(ctx, task.ID); err != nil {
			log.Warn().Err(err).Str("task_id", task.ID).Msg("monitorCancelled: failed to clear task assignment")
		}

		o.markRetryReady(ctx, task, notify.EventTaskCancelled)

		log.Info().Str("task_id", task.ID).Msg("cleaned up cancelled task")
	}
}

// monitorRunning checks each running task for timeouts and inspects its
// container status, handling completions and inspect errors.
func (o *Orchestrator) monitorRunning(ctx context.Context) {
	running := models.TaskStatusRunning
	tasks, err := o.store.ListTasks(ctx, store.TaskFilter{Status: &running})
	if err != nil {
		log.Error().Err(err).Msg("failed to list running tasks")
		return
	}

	for _, task := range tasks {
		if o.isTimedOut(task) {
			log.Warn().Str("task_id", task.ID).Msg("task exceeded max runtime, killing")
			o.killTask(ctx, task, "exceeded max runtime")
			continue
		}

		status, err := o.docker.InspectContainer(ctx, task.ContainerID)
		if err != nil {
			o.handleInspectError(ctx, task, err)
			continue
		}

		delete(o.inspectFailures, task.ID)

		if status.Done {
			o.saveAgentOutput(ctx, task)
			o.handleCompletion(ctx, task, status)
			o.saveOutputMetadata(ctx, task)
		}
	}
}

// isTimedOut returns true if the task has exceeded its configured max runtime.
func (o *Orchestrator) isTimedOut(task *models.Task) bool {
	if task.StartedAt == nil || task.MaxRuntimeSec <= 0 {
		return false
	}
	deadline := task.StartedAt.Add(time.Duration(task.MaxRuntimeSec) * time.Second)
	return time.Now().UTC().After(deadline)
}

// handleInspectError processes a container inspect failure, killing the task
// after the configured number of consecutive failures.
func (o *Orchestrator) handleInspectError(ctx context.Context, task *models.Task, err error) {
	outcome, count := o.classifyInspectFailure(task.ID, err)
	switch outcome {
	case inspectExceededThreshold:
		log.Warn().Err(err).Str("task_id", task.ID).Int("consecutive_failures", count).Msg("inspect threshold reached, killing task")
		o.killTask(ctx, task, fmt.Sprintf("container unreachable after %d inspect failures: %v", count, err))
	default:
		log.Warn().Err(err).Str("task_id", task.ID).Int("consecutive_failures", count).Msg("failed to inspect container")
	}
}

// handleCompletion processes a finished container: determines success/failure/needs_input,
// updates the task, sends notifications, and releases the running slot.
func (o *Orchestrator) handleCompletion(ctx context.Context, task *models.Task, status ContainerStatus) {
	now := time.Now().UTC()

	elapsed := status.ElapsedTimeSec
	if elapsed <= 0 && task.StartedAt != nil {
		elapsed = int(now.Sub(*task.StartedAt).Seconds())
	}

	result := lifecycle.Result{
		PRURL:          status.PRURL,
		OutputURL:      task.OutputURL,
		CostUSD:        status.CostUSD,
		ElapsedTimeSec: elapsed,
		RepoURL:        status.RepoURL,
		TargetBranch:   status.TargetBranch,
		TaskMode:       status.TaskMode,
	}

	switch {
	case status.Complete || (status.ExitCode == 0 && !status.NeedsInput):
		result.Status = models.TaskStatusCompleted
		result.EventType = notify.EventTaskCompleted
	case status.NeedsInput:
		result.Status = models.TaskStatusFailed
		result.Error = "agent needs input"
		result.EventType = notify.EventTaskNeedsInput
	default:
		result.Status = models.TaskStatusFailed
		result.Error = status.Error
		result.EventType = notify.EventTaskFailed
	}

	// Reading-mode completion: embed TL;DR and write the reading row synchronously.
	// If embedding or the DB write fails, the task itself fails.
	var readingOpt notify.EventOption
	if task.TaskMode == models.TaskModeRead && result.Status == models.TaskStatusCompleted {
		opt, err := o.handleReadingCompletion(ctx, task, status)
		if err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("handleReadingCompletion: reading pipeline failed")
			result.Status = models.TaskStatusFailed
			result.EventType = notify.EventTaskFailed
			result.Error = err.Error()
		} else {
			readingOpt = opt
		}
	}

	switch result.EventType {
	case notify.EventTaskCompleted:
		result.EventOpts = []notify.EventOption{notify.WithContainerStatus(status.PRURL, "", status.LogTail)}
		if readingOpt != nil {
			result.EventOpts = append(result.EventOpts, readingOpt)
		}
	case notify.EventTaskNeedsInput:
		result.EventOpts = []notify.EventOption{notify.WithContainerStatus("", status.Question, status.LogTail)}
	default:
		result.EventOpts = []notify.EventOption{notify.WithContainerStatus("", result.Error, status.LogTail)}
	}

	// Chain self-review on a successful code task. We pre-compute the child
	// from the projected post-completion parent state and let lifecycle.Complete
	// run the parent COMPLETE + child INSERT atomically.
	if result.Status == models.TaskStatusCompleted {
		projected := *task
		projected.Status = models.TaskStatusCompleted
		projected.PRURL = result.PRURL
		if result.RepoURL != "" {
			projected.RepoURL = result.RepoURL
		}
		if result.TaskMode != "" {
			projected.TaskMode = result.TaskMode
		}
		if child, ok := chain.Plan(&projected); ok {
			// Plan leaves MaxRuntimeSec/MaxTurns/AgentImage zero — fill them
			// from review-mode defaults so the chained task gets bounded
			// runtime/turn caps. Force CreatePR=false and SelfReview=false on
			// the child (the recursion guard in Plan covers SelfReview, but
			// being explicit avoids picking up the global default), and pin
			// SaveAgentOutput to whatever the parent had so we don't drift to
			// the global default for chained children.
			falseVal := false
			saveOutput := projected.SaveAgentOutput
			o.config.TaskDefaults(models.TaskModeReview).Apply(child, &config.BoolOverrides{
				CreatePR:        &falseVal,
				SelfReview:      &falseVal,
				SaveAgentOutput: &saveOutput,
			})
			log.Info().
				Str("parent_task_id", task.ID).
				Str("child_task_id", child.ID).
				Str("pr_url", result.PRURL).
				Msg("self-review chain planned")
			result.ChainTx = func(txCtx context.Context, tx store.Store) (*models.Task, error) {
				return child, chain.CreateChild(txCtx, tx, child)
			}
		}
	}

	if err := o.lifecycle.Complete(ctx, task, result); err != nil {
		// Complete already logs; nothing else to do here — it still released
		// the slot and emitted the event via its fallback path.
		_ = err
	}

	log.Info().Str("task_id", task.ID).Str("status", string(result.Status)).Msg("task completed")
}

// handleReadingCompletion embeds the agent's final TL;DR and writes the reading
// row. Returns an EventOption that populates the reading fields on the
// task.completed event. Any error here causes the task itself to fail.
func (o *Orchestrator) handleReadingCompletion(ctx context.Context, task *models.Task, status ContainerStatus) (notify.EventOption, error) {
	if o.embedder == nil {
		return nil, fmt.Errorf("reading completion: no embedder configured")
	}
	url := status.URL
	if url == "" {
		url = task.Prompt // read-mode tasks always have the URL as the prompt
	}
	if url == "" {
		return nil, fmt.Errorf("reading completion: agent reported empty url")
	}

	// Agent confirmed the URL already exists — complete without overwriting
	// the existing reading (which has richer content than the duplicate stub).
	if status.NoveltyVerdict == "duplicate" && !task.Force {
		return notify.WithReading(status.TLDR, status.NoveltyVerdict, status.Tags, status.Connections), nil
	}

	vec, err := o.embedder.Embed(ctx, status.TLDR)
	if err != nil {
		return nil, fmt.Errorf("embed tldr: %w", err)
	}

	raw, err := json.Marshal(agentStatusFromContainer(status))
	if err != nil {
		return nil, fmt.Errorf("marshal raw_output: %w", err)
	}

	reading := &models.Reading{
		ID:             "bf_" + ulid.Make().String(),
		TaskID:         task.ID,
		URL:            url,
		Title:          status.Title,
		TLDR:           status.TLDR,
		Tags:           status.Tags,
		Keywords:       status.Keywords,
		People:         status.People,
		Orgs:           status.Orgs,
		NoveltyVerdict: status.NoveltyVerdict,
		Connections:    status.Connections,
		Summary:        status.SummaryMarkdown,
		RawOutput:      raw,
		Embedding:      vec,
		CreatedAt:      time.Now().UTC(),
	}

	if err := o.store.UpsertReading(ctx, reading); err != nil {
		return nil, fmt.Errorf("upsert reading: %w", err)
	}

	return notify.WithReading(status.TLDR, status.NoveltyVerdict, status.Tags, status.Connections), nil
}

// agentStatusFromContainer reconstructs the reading-relevant portion of the
// agent's status output from the parsed ContainerStatus. Used to populate the
// reading's raw_output JSON text losslessly without depending on the original bytes.
func agentStatusFromContainer(s ContainerStatus) AgentStatus {
	return AgentStatus{
		Complete:        s.Complete,
		PRURL:           s.PRURL,
		CostUSD:         s.CostUSD,
		ElapsedTimeSec:  s.ElapsedTimeSec,
		RepoURL:         s.RepoURL,
		TargetBranch:    s.TargetBranch,
		TaskMode:        s.TaskMode,
		URL:             s.URL,
		Title:           s.Title,
		TLDR:            s.TLDR,
		Tags:            s.Tags,
		Keywords:        s.Keywords,
		People:          s.People,
		Orgs:            s.Orgs,
		NoveltyVerdict:  s.NoveltyVerdict,
		Connections:     s.Connections,
		SummaryMarkdown: s.SummaryMarkdown,
	}
}

// saveAgentOutput extracts the agent's output log from the container and
// writes it via the configured Writer if the task has save_agent_output
// enabled.
func (o *Orchestrator) saveAgentOutput(ctx context.Context, task *models.Task) {
	if !task.SaveAgentOutput || o.outputs == nil {
		return
	}

	data, err := o.docker.GetAgentOutput(ctx, task.ContainerID)
	if err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failed to extract agent output log")
		return
	}

	url, err := o.outputs.SaveLog(ctx, task.ID, []byte(data))
	if err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failed to save agent output")
		return
	}

	task.OutputURL = url
	log.Debug().Str("task_id", task.ID).Str("url", url).Msg("saved agent output")
}

// saveOutputMetadata writes the final task metadata snapshot (task.json)
// alongside the already-persisted agent log.
func (o *Orchestrator) saveOutputMetadata(ctx context.Context, task *models.Task) {
	if !task.SaveAgentOutput || o.outputs == nil {
		return
	}

	if err := o.outputs.SaveMetadata(ctx, task.ID, taskMetadataFrom(task)); err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failed to save output metadata")
		return
	}

	log.Debug().Str("task_id", task.ID).Msg("saved output metadata")
}

// taskMetadata is the subset of task fields written to disk (task.json) after
// completion. It excludes potentially sensitive fields like EnvVars and ClaudeMD.
type taskMetadata struct {
	ID            string            `json:"id"`
	Status        models.TaskStatus `json:"status"`
	TaskMode      string            `json:"task_mode"`
	Harness       models.Harness    `json:"harness"`
	RepoURL       string            `json:"repo_url"`
	Branch        string            `json:"branch"`
	TargetBranch  string            `json:"target_branch,omitempty"`
	Prompt        string            `json:"prompt"`
	Model         string            `json:"model,omitempty"`
	Effort        string            `json:"effort,omitempty"`
	MaxBudgetUSD  float64           `json:"max_budget_usd,omitempty"`
	MaxTurns      int               `json:"max_turns,omitempty"`
	MaxRuntimeSec int               `json:"max_runtime_sec,omitempty"`
	CreatePR      bool              `json:"create_pr"`
	SelfReview    bool              `json:"self_review"`
	PRURL         string            `json:"pr_url,omitempty"`
	OutputURL     string            `json:"output_url,omitempty"`
	CostUSD       float64           `json:"cost_usd,omitempty"`
	Error         string            `json:"error,omitempty"`
	RetryCount    int               `json:"retry_count"`
	CreatedAt     time.Time         `json:"created_at"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
}

// taskMetadataFrom projects a task row onto the external taskMetadata shape.
func taskMetadataFrom(task *models.Task) taskMetadata {
	return taskMetadata{
		ID:            task.ID,
		Status:        task.Status,
		TaskMode:      task.TaskMode,
		Harness:       task.Harness,
		RepoURL:       task.RepoURL,
		Branch:        task.Branch,
		TargetBranch:  task.TargetBranch,
		Prompt:        task.Prompt,
		Model:         task.Model,
		Effort:        task.Effort,
		MaxBudgetUSD:  task.MaxBudgetUSD,
		MaxTurns:      task.MaxTurns,
		MaxRuntimeSec: task.MaxRuntimeSec,
		CreatePR:      task.CreatePR,
		SelfReview:    task.SelfReview,
		PRURL:         task.PRURL,
		OutputURL:     task.OutputURL,
		CostUSD:       task.CostUSD,
		Error:         task.Error,
		RetryCount:    task.RetryCount,
		CreatedAt:     task.CreatedAt,
		StartedAt:     task.StartedAt,
		CompletedAt:   task.CompletedAt,
	}
}

// markRetryReady marks the task as cleanup-complete and emits the appropriate
// event. Always sets ready_for_retry=true so the monitor won't re-process
// this task on subsequent ticks. The retry cap is enforced by the atomic
// store.RetryTask WHERE clause, not by this flag.
func (o *Orchestrator) markRetryReady(ctx context.Context, task *models.Task, eventType notify.EventType, extraOpts ...notify.EventOption) {
	if err := o.lifecycle.MarkReadyForRetry(ctx, task.ID); err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("markRetryReady: failed to mark task ready for retry")
	}

	if task.UserRetryCount < o.config.MaxUserRetries {
		opts := append([]notify.EventOption{notify.WithReadyForRetry()}, extraOpts...)
		o.bus.Emit(notify.NewEvent(eventType, task, opts...))
	} else {
		opts := append([]notify.EventOption{notify.WithRetryLimitReached()}, extraOpts...)
		o.bus.Emit(notify.NewEvent(eventType, task, opts...))
	}
}

// killTask stops the container, marks the task as failed, and releases the slot.
func (o *Orchestrator) killTask(ctx context.Context, task *models.Task, reason string) {
	if task.ContainerID != "" {
		if err := o.docker.StopContainer(ctx, task.ContainerID); err != nil {
			log.Warn().Err(err).Str("task_id", task.ID).Msg("killTask: failed to stop container")
		}
	}

	elapsed := 0
	if task.StartedAt != nil {
		elapsed = int(time.Since(*task.StartedAt).Seconds())
	}
	if err := o.lifecycle.Complete(ctx, task, lifecycle.Result{
		Status:         models.TaskStatusFailed,
		EventType:      notify.EventTaskFailed,
		Error:          reason,
		ElapsedTimeSec: elapsed,
		EventOpts:      []notify.EventOption{notify.WithContainerStatus("", reason, "")},
	}); err != nil {
		_ = err
	}
}

// requeueTask resets a running task back to pending so it will be dispatched
// to a fresh container on the next tick. Thin wrapper around the coordinator —
// retained for callers inside monitor.go so the error-log context stays here.
func (o *Orchestrator) requeueTask(ctx context.Context, task *models.Task, reason string) {
	if err := o.lifecycle.Requeue(ctx, task, reason, lifecycle.RequeueInterrupted); err != nil {
		log.Error().Err(err).Str("task_id", task.ID).Msg("failed to re-queue task")
	}
}
