package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
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
			continue
		}

		o.docker.StopContainer(ctx, task.InstanceID, task.ContainerID)
		o.releaseSlot(ctx, task)

		// Clear assignment so we don't process this task again
		o.store.ClearTaskAssignment(ctx, task.ID)

		// Re-emit cancelled event with ReadyForRetry so Discord shows the Retry button
		o.bus.Emit(notify.NewEvent(notify.EventTaskCancelled, task, notify.WithReadyForRetry()))

		log.Info().Str("task_id", task.ID).Msg("cleaned up cancelled task")
	}
}

// monitorRunning checks each running task for timeouts and inspects its
// container status, handling completions, instance failures, and inspect errors.
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

		status, err := o.docker.InspectContainer(ctx, task.InstanceID, task.ContainerID)
		if err != nil {
			o.handleInspectError(ctx, task, err)
			continue
		}

		delete(o.inspectFailures, task.ID)

		if status.Done {
			o.saveAgentOutput(ctx, task)
			o.handleCompletion(ctx, task, status)
			o.saveTaskMetadata(ctx, task)
		}
	}
}

// isTimedOut returns true if the task has exceeded its configured max runtime.
func (o *Orchestrator) isTimedOut(task *models.Task) bool {
	if task.StartedAt == nil || task.MaxRuntimeMin <= 0 {
		return false
	}
	deadline := task.StartedAt.Add(time.Duration(task.MaxRuntimeMin) * time.Minute)
	return time.Now().UTC().After(deadline)
}

// handleInspectError processes a container inspect failure, requeuing on instance
// loss or killing the task after 3 consecutive failures.
func (o *Orchestrator) handleInspectError(ctx context.Context, task *models.Task, err error) {
	if IsInstanceGone(err) {
		log.Warn().Err(err).Str("task_id", task.ID).Str("instance", task.InstanceID).Msg("instance terminated, re-queuing task")
		delete(o.inspectFailures, task.ID)
		o.requeueTask(ctx, task, "instance terminated")
		return
	}

	o.inspectFailures[task.ID]++
	count := o.inspectFailures[task.ID]
	log.Warn().Err(err).Str("task_id", task.ID).Int("consecutive_failures", count).Msg("failed to inspect container")
	if count >= maxInspectFailures {
		delete(o.inspectFailures, task.ID)
		o.killTask(ctx, task, fmt.Sprintf("container unreachable after %d inspect failures: %v", count, err))
	}
}

// handleCompletion processes a finished container: determines success/failure/needs_input,
// updates the task, sends notifications, and releases the instance slot.
func (o *Orchestrator) handleCompletion(ctx context.Context, task *models.Task, status ContainerStatus) {
	now := time.Now().UTC()

	elapsed := status.ElapsedTimeSec
	if elapsed <= 0 && task.StartedAt != nil {
		elapsed = int(now.Sub(*task.StartedAt).Seconds())
	}

	result := store.TaskResult{
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
		o.bus.Emit(notify.NewEvent(notify.EventTaskCompleted, task, notify.WithContainerStatus(status.PRURL, "", status.LogTail)))
	case status.NeedsInput:
		result.Status = models.TaskStatusFailed
		result.Error = "agent needs input"
		o.bus.Emit(notify.NewEvent(notify.EventTaskNeedsInput, task, notify.WithContainerStatus("", status.Question, status.LogTail)))
	default:
		result.Status = models.TaskStatusFailed
		result.Error = status.Error
		o.bus.Emit(notify.NewEvent(notify.EventTaskFailed, task, notify.WithContainerStatus("", status.Error, status.LogTail)))
	}

	o.store.CompleteTask(ctx, task.ID, result)
	o.releaseSlot(ctx, task)

	log.Info().Str("task_id", task.ID).Str("status", string(result.Status)).Msg("task completed")
}

// saveAgentOutput extracts the agent's output log from the container and uploads
// it to S3 if the task has save_agent_output enabled and S3 is configured.
func (o *Orchestrator) saveAgentOutput(ctx context.Context, task *models.Task) {
	if !task.SaveAgentOutput || o.s3 == nil {
		return
	}

	data, err := o.docker.GetAgentOutput(ctx, task.InstanceID, task.ContainerID)
	if err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failed to extract agent output log")
		return
	}

	key := fmt.Sprintf("tasks/%s/agent_output.log", task.ID)
	url, err := o.s3.Upload(ctx, key, []byte(data))
	if err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failed to upload agent output to S3")
		return
	}

	task.OutputURL = url
	log.Debug().Str("task_id", task.ID).Str("url", url).Msg("saved agent output to S3")
}

// taskMetadata is the subset of task fields written to S3 after completion.
// It excludes potentially sensitive fields like EnvVars and ClaudeMD.
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
	MaxRuntimeMin int               `json:"max_runtime_min,omitempty"`
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

// saveTaskMetadata uploads a JSON summary of the completed task to S3,
// stored alongside the agent output under tasks/{taskID}/task_metadata.json.
func (o *Orchestrator) saveTaskMetadata(ctx context.Context, task *models.Task) {
	if o.s3 == nil {
		return
	}

	meta := taskMetadata{
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
		MaxRuntimeMin: task.MaxRuntimeMin,
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

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failed to marshal task metadata")
		return
	}

	key := fmt.Sprintf("tasks/%s/task_metadata.json", task.ID)
	_, err = o.s3.UploadJSON(ctx, key, data)
	if err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failed to upload task metadata to S3")
		return
	}

	log.Debug().Str("task_id", task.ID).Msg("saved task metadata to S3")
}

// killTask stops the container, marks the task as failed, and releases the slot.
func (o *Orchestrator) killTask(ctx context.Context, task *models.Task, reason string) {
	if task.ContainerID != "" {
		o.docker.StopContainer(ctx, task.InstanceID, task.ContainerID)
	}

	elapsed := 0
	if task.StartedAt != nil {
		elapsed = int(time.Since(*task.StartedAt).Seconds())
	}
	o.store.CompleteTask(ctx, task.ID, store.TaskResult{
		Status:         models.TaskStatusFailed,
		Error:          reason,
		ElapsedTimeSec: elapsed,
	})

	o.releaseSlot(ctx, task)

	o.bus.Emit(notify.NewEvent(notify.EventTaskFailed, task, notify.WithContainerStatus("", reason, "")))
}

// requeueTask resets a running task back to pending so it will be dispatched
// to a different instance. It also marks the old instance as terminated.
func (o *Orchestrator) requeueTask(ctx context.Context, task *models.Task, reason string) {
	if task.InstanceID != "" && o.config.Mode == config.ModeEC2 {
		o.markInstanceTerminated(ctx, task.InstanceID)
	}
	o.decrementRunning()

	if err := o.store.RequeueTask(ctx, task.ID, reason); err != nil {
		log.Error().Err(err).Str("task_id", task.ID).Msg("failed to re-queue task")
	}

	o.scaler.RequestScaleUp(ctx)
}
