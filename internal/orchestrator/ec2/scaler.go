package ec2

import (
	"context"
	"fmt"
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

// ec2Client is the subset of Manager methods used by Scaler, extracted as an
// interface to allow testing without real AWS calls.
type ec2Client interface {
	LaunchSpotInstance(ctx context.Context) (string, error)
	TerminateInstance(ctx context.Context, instanceID string) error
	DescribeInstance(ctx context.Context, instanceID string) (*types.Instance, error)
}

// Scaler manages EC2 instance scaling: launching new instances when capacity is
// needed, promoting pending instances when ready, and terminating idle ones.
type Scaler struct {
	store     store.Store
	ec2       ec2Client
	config    *config.Config
	ssmClient *ssm.Client
}

func NewScaler(s store.Store, ec2 *Manager, cfg *config.Config) *Scaler {
	return &Scaler{store: s, ec2: ec2, config: cfg}
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

// isDockerReady checks if Docker is running and the agent image is available
// by running a command on the instance via SSM.
func (s *Scaler) isDockerReady(ctx context.Context, instanceID string) bool {
	if err := s.ensureSSMClient(ctx); err != nil {
		return false
	}

	result, err := s.ssmClient.SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:  []string{instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters:   map[string][]string{"commands": {fmt.Sprintf("docker image inspect %s >/dev/null 2>&1 && echo ready", s.config.AgentImage)}},
	})
	if err != nil {
		return false
	}

	invocationInput := &ssm.GetCommandInvocationInput{
		CommandId:  result.Command.CommandId,
		InstanceId: aws.String(instanceID),
	}

	waiter := ssm.NewCommandExecutedWaiter(s.ssmClient)
	if err := waiter.Wait(ctx, invocationInput, 5*time.Minute); err != nil {
		return false
	}

	out, err := s.ssmClient.GetCommandInvocation(ctx, invocationInput)
	if err != nil {
		return false
	}
	return strings.TrimSpace(aws.ToString(out.StandardOutputContent)) == "ready"
}

// Evaluate checks pending instances, and scales down idle ones.
func (s *Scaler) Evaluate(ctx context.Context) {
	s.reconcilePending(ctx)
	s.reconcileRunning(ctx)
	s.scaleDown(ctx)
}

// RequestScaleUp launches a new instance if under the max and none are already pending.
func (s *Scaler) RequestScaleUp(ctx context.Context) {
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
		log.Error().Err(err).Str("instance_id", instanceID).Msg("scaler: failed to save instance, terminating orphan")
		if termErr := s.ec2.TerminateInstance(ctx, instanceID); termErr != nil {
			log.Error().Err(termErr).Str("instance_id", instanceID).Msg("scaler: failed to terminate orphaned instance")
		}
		return
	}

	log.Info().Str("instance_id", instanceID).Msg("scaler: launched new instance")
}

func (s *Scaler) reconcilePending(ctx context.Context) {
	pending := models.InstanceStatusPending
	instances, err := s.store.ListInstances(ctx, &pending)
	if err != nil {
		return
	}

	for _, inst := range instances {
		ec2Inst, err := s.ec2.DescribeInstance(ctx, inst.InstanceID)
		if err != nil {
			log.Warn().Err(err).Str("instance_id", inst.InstanceID).Msg("scaler: failed to describe pending instance")
			if time.Since(inst.CreatedAt) > 5*time.Minute {
				s.store.UpdateInstanceStatus(ctx, inst.InstanceID, models.InstanceStatusTerminated)
			}
			continue
		}

		switch ec2Inst.State.Name {
		case types.InstanceStateNameRunning:
			if !s.isSSMReady(ctx, inst.InstanceID) {
				log.Debug().Str("instance_id", inst.InstanceID).Msg("scaler: instance running but SSM not ready yet")
				break
			}
			if !s.isDockerReady(ctx, inst.InstanceID) {
				log.Debug().Str("instance_id", inst.InstanceID).Msg("scaler: instance running but Docker/image not ready yet")
				break
			}
			var ip, az string
			if ec2Inst.PrivateIpAddress != nil {
				ip = *ec2Inst.PrivateIpAddress
			}
			if ec2Inst.Placement != nil && ec2Inst.Placement.AvailabilityZone != nil {
				az = *ec2Inst.Placement.AvailabilityZone
			}
			s.store.UpdateInstanceStatus(ctx, inst.InstanceID, models.InstanceStatusRunning)
			s.store.UpdateInstanceDetails(ctx, inst.InstanceID, ip, az)
			log.Info().Str("instance_id", inst.InstanceID).Str("ip", ip).Msg("scaler: instance ready")

		case types.InstanceStateNameTerminated, types.InstanceStateNameShuttingDown:
			s.store.UpdateInstanceStatus(ctx, inst.InstanceID, models.InstanceStatusTerminated)
			log.Warn().Str("instance_id", inst.InstanceID).Msg("scaler: pending instance terminated")

		default:
			// Still pending, do nothing
		}
	}
}

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
			s.store.UpdateInstanceStatus(ctx, inst.InstanceID, models.InstanceStatusTerminated)
			continue
		}

		if ec2Inst.State.Name == types.InstanceStateNameTerminated || ec2Inst.State.Name == types.InstanceStateNameShuttingDown {
			log.Warn().Str("instance_id", inst.InstanceID).Str("ec2_state", string(ec2Inst.State.Name)).Msg("scaler: running instance was terminated externally")
			s.store.UpdateInstanceStatus(ctx, inst.InstanceID, models.InstanceStatusTerminated)
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

		s.store.UpdateInstanceStatus(ctx, inst.InstanceID, models.InstanceStatusTerminated)
	}
}
