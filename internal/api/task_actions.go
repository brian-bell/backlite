package api

import (
	"context"
	"fmt"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

// CancelTask validates the task is in a cancellable state, cancels it in the store,
// and emits a task.cancelled event. This is the shared implementation used by the
// REST API handler.
func CancelTask(ctx context.Context, taskID string, s store.Store, bus notify.Emitter) error {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}

	switch task.Status {
	case models.TaskStatusPending, models.TaskStatusProvisioning, models.TaskStatusRunning, models.TaskStatusRecovering:
		if err := s.CancelTask(ctx, taskID); err != nil {
			return err
		}
		if bus != nil {
			bus.Emit(notify.NewEvent(notify.EventTaskCancelled, task))
		}
		return nil
	default:
		return fmt.Errorf("task %s cannot be cancelled (status: %s)", taskID, task.Status)
	}
}

// RetryTask validates the task is in a retryable state and atomically requeues
// it. Pre-flight checks provide specific error messages; the actual requeue is
// protected by an atomic UPDATE ... WHERE ready_for_retry=true to prevent
// retries before cleanup and double-retries.
func RetryTask(ctx context.Context, taskID string, s store.Store, maxRetries int) error {
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}

	switch task.Status {
	case models.TaskStatusFailed, models.TaskStatusInterrupted, models.TaskStatusCancelled:
	default:
		return fmt.Errorf("task %s cannot be retried (status: %s)", taskID, task.Status)
	}

	if task.UserRetryCount >= maxRetries {
		return fmt.Errorf("task %s has reached the retry limit (%d/%d)", taskID, task.UserRetryCount, maxRetries)
	}

	if err := s.RetryTask(ctx, taskID, maxRetries); err != nil {
		return fmt.Errorf("task %s is still being cleaned up, try again shortly", taskID)
	}
	return nil
}
