package orchestrator

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

// dispatchPending finds pending tasks and dispatches them to available instances,
// up to the maximum concurrency limit.
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
			o.store.UpdateTaskStatus(ctx, task.ID, models.TaskStatusFailed, err.Error())
			o.bus.Emit(notify.NewEvent(notify.EventTaskFailed, task, notify.WithContainerStatus("", "Failed to dispatch: "+err.Error(), "")))
			continue
		}
	}
}

// dispatch assigns a task to an available instance, starts the container,
// and transitions the task from pending → provisioning → running.
func (o *Orchestrator) dispatch(ctx context.Context, task *models.Task) error {
	instance, err := o.findAvailableInstance(ctx)
	if err != nil {
		o.scaler.RequestScaleUp(ctx)
		return nil
	}

	if err := o.store.AssignTask(ctx, task.ID, instance.InstanceID); err != nil {
		return err
	}

	containerID, err := o.docker.RunAgent(ctx, instance, task)
	if err != nil {
		return err
	}

	if err := o.store.WithTx(ctx, func(tx store.Store) error {
		if err := tx.StartTask(ctx, task.ID, containerID); err != nil {
			return err
		}
		return tx.IncrementRunningContainers(ctx, instance.InstanceID)
	}); err != nil {
		return err
	}

	o.incrementRunning()

	o.bus.Emit(notify.NewEvent(notify.EventTaskRunning, task))

	log.Info().Str("task_id", task.ID).Str("container", containerID).Str("instance", instance.InstanceID).Msg("task dispatched")
	return nil
}

// findAvailableInstance returns the first running instance with spare container capacity.
func (o *Orchestrator) findAvailableInstance(ctx context.Context) (*models.Instance, error) {
	running := models.InstanceStatusRunning
	instances, err := o.store.ListInstances(ctx, &running)
	if err != nil {
		return nil, err
	}

	for _, inst := range instances {
		if inst.RunningContainers < inst.MaxContainers {
			return inst, nil
		}
	}

	return nil, errNoCapacity
}
