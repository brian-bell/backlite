package orchestrator

import (
	"context"
	"errors"
	"fmt"

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

// failReadDuplicate short-circuits a read-mode dispatch when the URL is
// already present in the readings table and the task did not request Force.
// Marks the task failed with a user-actionable message and emits task.failed
// directly. Returns nil so dispatchPending does not treat this as a dispatch
// error (the task is already in its terminal state and the event is emitted).
func (o *Orchestrator) failReadDuplicate(ctx context.Context, task *models.Task, existing *models.Reading) error {
	msg := fmt.Sprintf("reading already exists for url %q (id=%s); resubmit with force=true to overwrite", task.Prompt, existing.ID)
	if err := o.lifecycle.FailDispatch(ctx, task, msg); err != nil {
		log.Warn().Err(err).Str("task_id", task.ID).Msg("failReadDuplicate: FailDispatch returned error")
	}
	log.Info().Str("task_id", task.ID).Str("url", task.Prompt).Str("existing_reading_id", existing.ID).Msg("read task short-circuited: duplicate URL")
	return nil
}
