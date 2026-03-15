package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

var errNoCapacity = fmt.Errorf("no instance capacity available")

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
			task.Status = models.TaskStatusFailed
			task.Error = err.Error()
			o.store.UpdateTask(ctx, task)
			o.notifier.Notify(notify.Event{
				Type:      notify.EventTaskFailed,
				TaskID:    task.ID,
				RepoURL:   task.RepoURL,
				Prompt:    task.Prompt,
				Message:   "Failed to dispatch: " + err.Error(),
				Timestamp: time.Now().UTC(),
			})
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

	task.Status = models.TaskStatusProvisioning
	task.InstanceID = instance.InstanceID
	if err := o.store.UpdateTask(ctx, task); err != nil {
		return err
	}

	containerID, err := o.docker.RunAgent(ctx, instance, task)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	task.Status = models.TaskStatusRunning
	task.ContainerID = containerID
	task.StartedAt = &now
	if err := o.store.UpdateTask(ctx, task); err != nil {
		return err
	}

	o.incrementRunning()

	instance.RunningContainers++
	o.store.UpdateInstance(ctx, instance)

	o.notifier.Notify(notify.Event{
		Type:      notify.EventTaskRunning,
		TaskID:    task.ID,
		RepoURL:   task.RepoURL,
		Prompt:    task.Prompt,
		Timestamp: now,
	})

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
