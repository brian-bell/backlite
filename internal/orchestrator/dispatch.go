package orchestrator

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/orchestrator/imagerouter"
	"github.com/brian-bell/backlite/internal/store"
)

// dispatchPending finds pending tasks and dispatches them, up to the maximum
// concurrency limit.
func (o *Orchestrator) dispatchPending(ctx context.Context) {
	o.mu.Lock()
	available := o.config.MaxConcurrent() - o.running
	o.mu.Unlock()

	if available <= 0 {
		return
	}

	pending := models.TaskStatusPending
	tasks, err := o.store.ListTasks(ctx, store.TaskFilter{
		Status: &pending,
		Limit:  available,
	})
	if err != nil {
		log.Error().Err(err).Msg("failed to list pending tasks")
		return
	}

	for _, task := range tasks {
		if err := o.dispatch(ctx, task); err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("failed to dispatch task")
			if ferr := o.lifecycle.FailDispatch(ctx, task, err.Error()); ferr != nil {
				log.Warn().Err(ferr).Str("task_id", task.ID).Msg("dispatchPending: FailDispatch returned error")
			}
			continue
		}
	}
}

// dispatch assigns a task, starts its container, and transitions
// pending → provisioning → running.
func (o *Orchestrator) dispatch(ctx context.Context, task *models.Task) error {
	task.AgentImage = imagerouter.Resolve(task, o.config)

	if err := o.lifecycle.Assign(ctx, task.ID); err != nil {
		return err
	}

	containerID, err := o.docker.RunAgent(ctx, task)
	if err != nil {
		return err
	}

	if err := o.lifecycle.Start(ctx, task, containerID); err != nil {
		return err
	}

	log.Info().Str("task_id", task.ID).Str("container", containerID).Msg("task dispatched")
	return nil
}
