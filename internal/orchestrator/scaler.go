package orchestrator

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

const idleTimeout = 5 * time.Minute

type Scaler struct {
	store  store.Store
	ec2    *EC2Manager
	config *config.Config
}

func NewScaler(s store.Store, ec2 *EC2Manager, cfg *config.Config) *Scaler {
	return &Scaler{store: s, ec2: ec2, config: cfg}
}

// Evaluate checks if we need to scale up or down.
func (s *Scaler) Evaluate(ctx context.Context) {
	s.scaleDown(ctx)
}

// RequestScaleUp launches a new instance if under the max.
func (s *Scaler) RequestScaleUp(ctx context.Context) {
	running := models.InstanceStatusRunning
	instances, err := s.store.ListInstances(ctx, &running)
	if err != nil {
		log.Error().Err(err).Msg("scaler: failed to list instances")
		return
	}

	if len(instances) >= s.config.MaxInstances {
		log.Debug().Int("running", len(instances)).Int("max", s.config.MaxInstances).Msg("scaler: at max instances")
		return
	}

	instanceID, err := s.ec2.LaunchSpotInstance(ctx)
	if err != nil {
		log.Error().Err(err).Msg("scaler: failed to launch instance")
		return
	}

	now := time.Now().UTC()
	inst := &models.Instance{
		InstanceID:    instanceID,
		InstanceType:  s.config.InstanceType,
		Status:        models.InstanceStatusPending,
		MaxContainers: s.config.ContainersPerInst,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.store.CreateInstance(ctx, inst); err != nil {
		log.Error().Err(err).Str("instance_id", instanceID).Msg("scaler: failed to save instance")
		return
	}

	log.Info().Str("instance_id", instanceID).Msg("scaler: launched new instance")
}

func (s *Scaler) scaleDown(ctx context.Context) {
	running := models.InstanceStatusRunning
	instances, err := s.store.ListInstances(ctx, &running)
	if err != nil {
		return
	}

	for _, inst := range instances {
		if inst.RunningContainers > 0 {
			continue
		}

		idle := time.Since(inst.UpdatedAt)
		if idle < idleTimeout {
			continue
		}

		log.Info().Str("instance_id", inst.InstanceID).Dur("idle", idle).Msg("scaler: terminating idle instance")

		if err := s.ec2.TerminateInstance(ctx, inst.InstanceID); err != nil {
			log.Error().Err(err).Str("instance_id", inst.InstanceID).Msg("scaler: failed to terminate")
			continue
		}

		inst.Status = models.InstanceStatusTerminated
		s.store.UpdateInstance(ctx, inst)
	}
}
