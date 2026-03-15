package orchestrator

import (
	"context"
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

		// Clear ContainerID so we don't process this task again
		task.ContainerID = ""
		o.store.UpdateTask(ctx, task)

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
			o.handleCompletion(ctx, task, status)
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
	if isInstanceGone(err) {
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
	task.CompletedAt = &now
	task.PRURL = status.PRURL

	switch {
	case status.ExitCode == 0:
		task.Status = models.TaskStatusCompleted
		o.notifier.Notify(notify.Event{
			Type:         notify.EventTaskCompleted,
			TaskID:       task.ID,
			RepoURL:      task.RepoURL,
			Prompt:       task.Prompt,
			PRURL:        status.PRURL,
			AgentLogTail: status.LogTail,
			Timestamp:    now,
		})
	case status.NeedsInput:
		task.Status = models.TaskStatusFailed
		task.Error = "agent needs input"
		o.notifier.Notify(notify.Event{
			Type:         notify.EventTaskNeedsInput,
			TaskID:       task.ID,
			RepoURL:      task.RepoURL,
			Prompt:       task.Prompt,
			Message:      status.Question,
			AgentLogTail: status.LogTail,
			Timestamp:    now,
		})
	default:
		task.Status = models.TaskStatusFailed
		task.Error = status.Error
		o.notifier.Notify(notify.Event{
			Type:         notify.EventTaskFailed,
			TaskID:       task.ID,
			RepoURL:      task.RepoURL,
			Prompt:       task.Prompt,
			Message:      status.Error,
			AgentLogTail: status.LogTail,
			Timestamp:    now,
		})
	}

	o.store.UpdateTask(ctx, task)
	o.releaseSlot(ctx, task)

	log.Info().Str("task_id", task.ID).Str("status", string(task.Status)).Msg("task completed")
}

// killTask stops the container, marks the task as failed, and releases the slot.
func (o *Orchestrator) killTask(ctx context.Context, task *models.Task, reason string) {
	if task.ContainerID != "" {
		o.docker.StopContainer(ctx, task.InstanceID, task.ContainerID)
	}

	now := time.Now().UTC()
	task.Status = models.TaskStatusFailed
	task.Error = reason
	task.CompletedAt = &now
	o.store.UpdateTask(ctx, task)

	o.releaseSlot(ctx, task)

	o.notifier.Notify(notify.Event{
		Type:      notify.EventTaskFailed,
		TaskID:    task.ID,
		RepoURL:   task.RepoURL,
		Prompt:    task.Prompt,
		Message:   reason,
		Timestamp: now,
	})
}

// requeueTask resets a running task back to pending so it will be dispatched
// to a different instance. It also marks the old instance as terminated.
func (o *Orchestrator) requeueTask(ctx context.Context, task *models.Task, reason string) {
	if task.InstanceID != "" && o.config.Mode != config.ModeLocal {
		o.markInstanceTerminated(ctx, task.InstanceID)
	}
	o.decrementRunning()

	task.Status = models.TaskStatusPending
	task.InstanceID = ""
	task.ContainerID = ""
	task.StartedAt = nil
	task.Error = "re-queued: " + reason + " at " + time.Now().UTC().Format(time.RFC3339)
	task.RetryCount++
	if err := o.store.UpdateTask(ctx, task); err != nil {
		log.Error().Err(err).Str("task_id", task.ID).Msg("failed to re-queue task")
	}

	o.scaler.RequestScaleUp(ctx)
}
