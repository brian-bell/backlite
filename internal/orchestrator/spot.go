package orchestrator

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

type SpotHandler struct {
	store store.Store
	ec2   *EC2Manager
}

func NewSpotHandler(s store.Store, ec2 *EC2Manager) *SpotHandler {
	return &SpotHandler{store: s, ec2: ec2}
}

// CheckInterruptions polls running instances for spot termination notices.
// Called periodically by the orchestrator.
func (h *SpotHandler) CheckInterruptions(ctx context.Context) {
	running := models.InstanceStatusRunning
	instances, err := h.store.ListInstances(ctx, &running)
	if err != nil {
		log.Error().Err(err).Msg("spot: failed to list running instances")
		return
	}

	for _, inst := range instances {
		interrupted, err := h.isInterrupted(ctx, inst.InstanceID)
		if err != nil {
			log.Warn().Err(err).Str("instance", inst.InstanceID).Msg("spot: failed to check interruption")
			continue
		}
		if interrupted {
			log.Warn().Str("instance", inst.InstanceID).Msg("spot: termination notice detected")
			h.handleInterruption(ctx, inst)
		}
	}
}

func (h *SpotHandler) isInterrupted(ctx context.Context, instanceID string) (bool, error) {
	inst, err := h.ec2.DescribeInstance(ctx, instanceID)
	if err != nil {
		return false, err
	}

	// Check if the instance state indicates it's being terminated
	if inst.State != nil {
		state := inst.State.Name
		if state == "shutting-down" || state == "terminated" {
			return true, nil
		}
	}

	return false, nil
}

func (h *SpotHandler) handleInterruption(ctx context.Context, inst *models.Instance) {
	// Mark instance as draining
	inst.Status = models.InstanceStatusDraining
	h.store.UpdateInstance(ctx, inst)

	// Find all running tasks on this instance and re-queue them
	running := models.TaskStatusRunning
	tasks, err := h.store.ListTasks(ctx, store.TaskFilter{Status: &running})
	if err != nil {
		log.Error().Err(err).Msg("spot: failed to list running tasks for re-queue")
		return
	}

	for _, task := range tasks {
		if task.InstanceID != inst.InstanceID {
			continue
		}

		log.Info().Str("task_id", task.ID).Msg("spot: re-queuing interrupted task")

		task.Status = models.TaskStatusPending
		task.InstanceID = ""
		task.ContainerID = ""
		task.StartedAt = nil
		task.Error = "re-queued due to spot interruption at " + time.Now().UTC().Format(time.RFC3339)
		task.RetryCount++

		if err := h.store.UpdateTask(ctx, task); err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("spot: failed to re-queue task")
		}
	}
}
