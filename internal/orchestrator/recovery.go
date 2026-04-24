package orchestrator

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/store"
)

// recoverOnStartup transitions orphaned running/provisioning tasks to the
// recovering status so they can be inspected by monitorRecovering on each tick.
func (o *Orchestrator) recoverOnStartup(ctx context.Context) {
	runningTasks := o.listTasksSafe(ctx, models.TaskStatusRunning, "running")
	provTasks := o.listTasksSafe(ctx, models.TaskStatusProvisioning, "provisioning")
	recoveringTasks := o.listTasksSafe(ctx, models.TaskStatusRecovering, "recovering")

	// Count already-recovering tasks that had a running container (from a prior restart).
	// These still count toward o.running since monitorRecovering will decrement
	// o.running when it requeues them.
	previouslyRunning := 0
	for _, task := range recoveringTasks {
		if task.ContainerID != "" {
			previouslyRunning++
		}
	}

	if len(runningTasks) == 0 && len(provTasks) == 0 && previouslyRunning == 0 {
		return
	}

	log.Info().Int("running", len(runningTasks)).Int("provisioning", len(provTasks)).Int("already_recovering", previouslyRunning).Msg("recovery: found orphaned tasks")

	// Provisioning tasks: mark recovering, clear instance/container
	// (dispatch never incremented o.running for these).
	for _, task := range provTasks {
		o.lifecycle.MarkRecovering(ctx, task, true, "recovering after server restart (was provisioning)")
	}

	// Running tasks: mark recovering, preserve instance/container for inspection.
	instanceContainers := make(map[string]int)
	for _, task := range runningTasks {
		o.lifecycle.MarkRecovering(ctx, task, false, "recovering after server restart (was running)")
		if task.InstanceID != "" {
			instanceContainers[task.InstanceID]++
		}
	}

	// Set o.running to the count of previously-running tasks plus any
	// already-recovering tasks that had containers (from a prior restart).
	o.mu.Lock()
	o.running = len(runningTasks) + previouslyRunning
	o.mu.Unlock()

	// Fix up RunningContainers for each referenced instance
	for instID, count := range instanceContainers {
		o.store.ResetRunningContainers(ctx, instID)
		for i := 0; i < count; i++ {
			o.store.IncrementRunningContainers(ctx, instID)
		}
	}

	log.Info().Int("recovering", len(runningTasks)+len(provTasks)).Msg("recovery: tasks marked as recovering")
}

// listTasksSafe lists tasks by status, returning nil on error (with logging).
func (o *Orchestrator) listTasksSafe(ctx context.Context, status models.TaskStatus, label string) []*models.Task {
	tasks, err := o.store.ListTasks(ctx, store.TaskFilter{Status: &status})
	if err != nil {
		log.Error().Err(err).Msgf("recovery: failed to list %s tasks", label)
		return nil
	}
	return tasks
}

// monitorRecovering checks recovering tasks and either promotes them back to
// running, completes them, or re-queues them to pending.
func (o *Orchestrator) monitorRecovering(ctx context.Context) {
	recovering := models.TaskStatusRecovering
	tasks, err := o.store.ListTasks(ctx, store.TaskFilter{Status: &recovering})
	if err != nil {
		log.Error().Err(err).Msg("failed to list recovering tasks")
		return
	}

	for _, task := range tasks {
		if task.ContainerID == "" {
			// Was provisioning — no container to inspect, re-queue immediately.
			log.Info().Str("task_id", task.ID).Msg("recovery: re-queuing task (was provisioning)")
			if err := o.lifecycle.Recover(ctx, task, false, "no container (was provisioning)"); err != nil {
				log.Error().Err(err).Str("task_id", task.ID).Msg("failed to re-queue recovering task")
			}
			continue
		}

		// Was running — try to inspect the container.
		status, err := o.docker.InspectContainer(ctx, task.InstanceID, task.ContainerID)
		if err != nil {
			o.handleRecoveringInspectError(ctx, task, err)
			continue
		}

		delete(o.inspectFailures, task.ID)

		if status.Done {
			log.Info().Str("task_id", task.ID).Msg("recovery: container exited, handling completion")
			o.handleCompletion(ctx, task, status)
		} else {
			// Container still running — promote back to running.
			log.Info().Str("task_id", task.ID).Msg("recovery: container still running, promoting to running")
			if err := o.lifecycle.Recover(ctx, task, true, ""); err != nil {
				log.Warn().Err(err).Str("task_id", task.ID).Msg("recovery: failed to promote to running")
			}
		}
	}
}

// handleRecoveringInspectError handles inspect failures for recovering tasks,
// requeuing on instance loss or after repeated failures.
func (o *Orchestrator) handleRecoveringInspectError(ctx context.Context, task *models.Task, err error) {
	outcome, count := o.classifyInspectFailure(task.ID, err)
	switch outcome {
	case inspectInstanceGone:
		log.Warn().Str("task_id", task.ID).Msg("recovery: instance gone, re-queuing")
		if err := o.lifecycle.Recover(ctx, task, false, "instance gone"); err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("failed to re-queue recovering task")
		}
	case inspectExceededThreshold:
		log.Warn().Err(err).Str("task_id", task.ID).Int("consecutive_failures", count).Msg("recovery: inspect failed repeatedly, re-queuing")
		if err := o.lifecycle.Recover(ctx, task, false, fmt.Sprintf("inspect error after %d failures: %v", count, err)); err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("failed to re-queue recovering task")
		}
	default:
		log.Warn().Err(err).Str("task_id", task.ID).Int("consecutive_failures", count).Msg("recovery: inspect failed")
	}
}
