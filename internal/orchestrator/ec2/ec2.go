package ec2

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
)

// Manager handles EC2 instance lifecycle (launch, terminate, describe).
type Manager struct {
	config *config.Config
	client *ec2.Client
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{config: cfg}
}

func (m *Manager) ensureClient(ctx context.Context) error {
	if m.client != nil {
		return nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(m.config.AWSRegion))
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	m.client = ec2.NewFromConfig(cfg)
	return nil
}

func (m *Manager) LaunchSpotInstance(ctx context.Context) (string, error) {
	if err := m.ensureClient(ctx); err != nil {
		return "", err
	}

	input := &ec2.RunInstancesInput{
		MinCount: aws.Int32(1),
		MaxCount: aws.Int32(1),
		InstanceMarketOptions: &types.InstanceMarketOptionsRequest{
			MarketType: types.MarketTypeSpot,
			SpotOptions: &types.SpotMarketOptions{
				SpotInstanceType:             types.SpotInstanceTypeOneTime,
				InstanceInterruptionBehavior: types.InstanceInterruptionBehaviorTerminate,
			},
		},
	}

	if m.config.LaunchTemplateID != "" {
		input.LaunchTemplate = &types.LaunchTemplateSpecification{
			LaunchTemplateId: aws.String(m.config.LaunchTemplateID),
			Version:          aws.String("$Latest"),
		}
	} else if m.config.AMI != "" {
		input.ImageId = aws.String(m.config.AMI)
		input.InstanceType = types.InstanceType(m.config.InstanceType)
		input.TagSpecifications = []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{Key: aws.String("Name"), Value: aws.String("backflow-agent")},
					{Key: aws.String("backflow"), Value: aws.String("true")},
				},
			},
		}
	} else {
		return "", fmt.Errorf("either BACKFLOW_AMI or BACKFLOW_LAUNCH_TEMPLATE_ID must be set")
	}

	result, err := m.client.RunInstances(ctx, input)
	if err != nil {
		return "", fmt.Errorf("run instances: %w", err)
	}

	if len(result.Instances) == 0 {
		return "", fmt.Errorf("no instances returned")
	}

	instanceID := aws.ToString(result.Instances[0].InstanceId)
	log.Info().Str("instance_id", instanceID).Str("type", m.config.InstanceType).Msg("launched spot instance")
	return instanceID, nil
}

func (m *Manager) TerminateInstance(ctx context.Context, instanceID string) error {
	if err := m.ensureClient(ctx); err != nil {
		return err
	}

	_, err := m.client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return fmt.Errorf("terminate instance %s: %w", instanceID, err)
	}

	log.Info().Str("instance_id", instanceID).Msg("terminated instance")
	return nil
}

func (m *Manager) DescribeInstance(ctx context.Context, instanceID string) (*types.Instance, error) {
	if err := m.ensureClient(ctx); err != nil {
		return nil, err
	}

	result, err := m.client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return nil, fmt.Errorf("describe instance %s: %w", instanceID, err)
	}

	for _, r := range result.Reservations {
		for _, inst := range r.Instances {
			return &inst, nil
		}
	}

	return nil, fmt.Errorf("instance %s not found", instanceID)
}
