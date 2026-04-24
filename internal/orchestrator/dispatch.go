package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
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
			if err := o.store.UpdateTaskStatus(ctx, task.ID, models.TaskStatusFailed, err.Error()); err != nil {
				log.Warn().Err(err).Str("task_id", task.ID).Msg("dispatchPending: failed to mark task as failed")
			}
			o.bus.Emit(notify.NewEvent(notify.EventTaskFailed, task, notify.WithContainerStatus("", "Failed to dispatch: "+err.Error(), "")))
			continue
		}
	}
}

// dispatch assigns a task to an available instance, starts the container,
// and transitions the task from pending → provisioning → running.
func (o *Orchestrator) dispatch(ctx context.Context, task *models.Task) error {
	if task.TaskMode == models.TaskModeRead {
		if o.embedder == nil {
			return fmt.Errorf("cannot dispatch read task: no embedder configured (set OPENAI_API_KEY)")
		}
		if o.config.ReaderImage == "" {
			return fmt.Errorf("cannot dispatch read task: no reader image configured (set BACKFLOW_READER_IMAGE)")
		}
		if !task.Force {
			existing, err := o.store.GetReadingByURL(ctx, task.Prompt)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("lookup reading by url: %w", err)
			}
			if existing != nil {
				return o.failReadDuplicate(ctx, task, existing)
			}
		}
	}

	instance, err := o.findAvailableInstance(ctx)
	if errors.Is(err, errNoCapacity) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find available instance: %w", err)
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

// failReadDuplicate short-circuits a read-mode dispatch when the URL is
// already present in the readings table and the task did not request Force.
// Marks the task failed with a user-actionable message and emits task.failed
// directly. Returns nil so dispatchPending does not treat this as a dispatch
// error (the task is already in its terminal state and the event is emitted).
func (o *Orchestrator) failReadDuplicate(ctx context.Context, task *models.Task, existing *models.Reading) error {
	msg := fmt.Sprintf("reading already exists for url %q (id=%s); resubmit with force=true to overwrite", task.Prompt, existing.ID)
	if err := o.store.UpdateTaskStatus(ctx, task.ID, models.TaskStatusFailed, msg); err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failReadDuplicate: failed to mark task failed")
	}
	o.bus.Emit(notify.NewEvent(notify.EventTaskFailed, task, notify.WithContainerStatus("", msg, "")))
	log.Info().Str("task_id", task.ID).Str("url", task.Prompt).Str("existing_reading_id", existing.ID).Msg("read task short-circuited: duplicate URL")
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
