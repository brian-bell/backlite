package ec2

import (
	"context"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

// SpotHandler polls running instances for spot termination notices and
// re-queues affected tasks.
type SpotHandler struct {
	store store.Store
	ec2   *Manager
}

func NewSpotHandler(s store.Store, ec2 *Manager) *SpotHandler {
	return &SpotHandler{store: s, ec2: ec2}
}

// CheckInterruptions polls running instances for spot termination notices.
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

	if inst.State != nil {
		state := inst.State.Name
		if state == "shutting-down" || state == "terminated" {
			return true, nil
		}
	}

	return false, nil
}

func (h *SpotHandler) handleInterruption(ctx context.Context, inst *models.Instance) {
	h.store.UpdateInstanceStatus(ctx, inst.InstanceID, models.InstanceStatusDraining)

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

		if err := h.store.RequeueTask(ctx, task.ID, "spot interruption"); err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("spot: failed to re-queue task")
		}
	}
}
