package orchestrator

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

const idleTimeout = 5 * time.Minute

type Scaler struct {
	store     store.Store
	ec2       *EC2Manager
	docker    *DockerManager
	config    *config.Config
	ssmClient *ssm.Client
}

func NewScaler(s store.Store, ec2 *EC2Manager, docker *DockerManager, cfg *config.Config) *Scaler {
	return &Scaler{store: s, ec2: ec2, docker: docker, config: cfg}
}

func (s *Scaler) ensureSSMClient(ctx context.Context) error {
	if s.ssmClient != nil {
		return nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(s.config.AWSRegion))
	if err != nil {
		return err
	}
	s.ssmClient = ssm.NewFromConfig(cfg)
	return nil
}

// isSSMReady checks if the SSM agent on the instance is online and ready.
func (s *Scaler) isSSMReady(ctx context.Context, instanceID string) bool {
	if err := s.ensureSSMClient(ctx); err != nil {
		return false
	}
	out, err := s.ssmClient.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []ssmtypes.InstanceInformationStringFilter{
			{Key: aws.String("InstanceIds"), Values: []string{instanceID}},
		},
	})
	if err != nil {
		return false
	}
	for _, info := range out.InstanceInformationList {
		if info.PingStatus == ssmtypes.PingStatusOnline {
			return true
		}
	}
	return false
}

// isDockerReady checks if Docker is running and the agent image is available.
func (s *Scaler) isDockerReady(ctx context.Context, instanceID string) bool {
	out, err := s.docker.runSSMCommand(ctx, instanceID, "docker image inspect backflow-agent:latest >/dev/null 2>&1 && echo ready")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "ready"
}

// Evaluate checks pending instances, and scales down idle ones.
func (s *Scaler) Evaluate(ctx context.Context) {
	s.reconcilePending(ctx)
	s.reconcileRunning(ctx)
	s.scaleDown(ctx)
}

// RequestScaleUp launches a new instance if under the max and none are already pending.
func (s *Scaler) RequestScaleUp(ctx context.Context) {
	// Count all non-terminated instances (pending + running)
	allInstances, err := s.store.ListInstances(ctx, nil)
	if err != nil {
		log.Error().Err(err).Msg("scaler: failed to list instances")
		return
	}

	active := 0
	for _, inst := range allInstances {
		if inst.Status == models.InstanceStatusPending || inst.Status == models.InstanceStatusRunning {
			active++
		}
		// Don't launch if there's already a pending instance booting up
		if inst.Status == models.InstanceStatusPending {
			log.Debug().Str("instance_id", inst.InstanceID).Msg("scaler: instance already pending, skipping scale-up")
			return
		}
	}

	if active >= s.config.MaxInstances {
		log.Debug().Int("active", active).Int("max", s.config.MaxInstances).Msg("scaler: at max instances")
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

// reconcilePending checks if pending instances have become running (or failed).
func (s *Scaler) reconcilePending(ctx context.Context) {
	pending := models.InstanceStatusPending
	instances, err := s.store.ListInstances(ctx, &pending)
	if err != nil {
		return
	}

	for _, inst := range instances {
		ec2Inst, err := s.ec2.DescribeInstance(ctx, inst.InstanceID)
		if err != nil {
			// If instance not found, mark terminated
			log.Warn().Err(err).Str("instance_id", inst.InstanceID).Msg("scaler: failed to describe pending instance")
			if time.Since(inst.CreatedAt) > 5*time.Minute {
				inst.Status = models.InstanceStatusTerminated
				s.store.UpdateInstance(ctx, inst)
			}
			continue
		}

		switch ec2Inst.State.Name {
		case types.InstanceStateNameRunning:
			// EC2 is running, but wait for SSM + Docker before accepting tasks
			if !s.isSSMReady(ctx, inst.InstanceID) {
				log.Debug().Str("instance_id", inst.InstanceID).Msg("scaler: instance running but SSM not ready yet")
				break
			}
			if !s.isDockerReady(ctx, inst.InstanceID) {
				log.Debug().Str("instance_id", inst.InstanceID).Msg("scaler: instance running but Docker/image not ready yet")
				break
			}
			inst.Status = models.InstanceStatusRunning
			if ec2Inst.PrivateIpAddress != nil {
				inst.PrivateIP = *ec2Inst.PrivateIpAddress
			}
			if ec2Inst.Placement != nil && ec2Inst.Placement.AvailabilityZone != nil {
				inst.AvailabilityZone = *ec2Inst.Placement.AvailabilityZone
			}
			s.store.UpdateInstance(ctx, inst)
			log.Info().Str("instance_id", inst.InstanceID).Str("ip", inst.PrivateIP).Msg("scaler: instance ready")

		case types.InstanceStateNameTerminated, types.InstanceStateNameShuttingDown:
			inst.Status = models.InstanceStatusTerminated
			s.store.UpdateInstance(ctx, inst)
			log.Warn().Str("instance_id", inst.InstanceID).Msg("scaler: pending instance terminated")

		default:
			// Still pending, do nothing
		}
	}
}

// reconcileRunning detects running instances that were terminated externally
// (e.g. spot interruption not caught by SpotHandler, manual termination).
func (s *Scaler) reconcileRunning(ctx context.Context) {
	running := models.InstanceStatusRunning
	instances, err := s.store.ListInstances(ctx, &running)
	if err != nil {
		return
	}

	for _, inst := range instances {
		ec2Inst, err := s.ec2.DescribeInstance(ctx, inst.InstanceID)
		if err != nil {
			log.Warn().Err(err).Str("instance_id", inst.InstanceID).Msg("scaler: failed to describe running instance, marking terminated")
			inst.Status = models.InstanceStatusTerminated
			inst.RunningContainers = 0
			s.store.UpdateInstance(ctx, inst)
			continue
		}

		if ec2Inst.State.Name == types.InstanceStateNameTerminated || ec2Inst.State.Name == types.InstanceStateNameShuttingDown {
			log.Warn().Str("instance_id", inst.InstanceID).Str("ec2_state", string(ec2Inst.State.Name)).Msg("scaler: running instance was terminated externally")
			inst.Status = models.InstanceStatusTerminated
			inst.RunningContainers = 0
			s.store.UpdateInstance(ctx, inst)
		}
	}
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
