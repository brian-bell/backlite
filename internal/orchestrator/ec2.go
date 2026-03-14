package orchestrator

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

type EC2Manager struct {
	config *config.Config
	client *ec2.Client
}

func NewEC2Manager(cfg *config.Config) *EC2Manager {
	return &EC2Manager{config: cfg}
}

func (m *EC2Manager) ensureClient(ctx context.Context) error {
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

func (m *EC2Manager) LaunchSpotInstance(ctx context.Context) (string, error) {
	if err := m.ensureClient(ctx); err != nil {
		return "", err
	}

	input := &ec2.RunInstancesInput{
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		InstanceType: types.InstanceType(m.config.InstanceType),
		InstanceMarketOptions: &types.InstanceMarketOptionsRequest{
			MarketType: types.MarketTypeSpot,
			SpotOptions: &types.SpotMarketOptions{
				SpotInstanceType:             types.SpotInstanceTypeOneTime,
				InstanceInterruptionBehavior: types.InstanceInterruptionBehaviorTerminate,
			},
		},
		TagSpecifications: []types.TagSpecification{
			{
				ResourceType: types.ResourceTypeInstance,
				Tags: []types.Tag{
					{Key: aws.String("Name"), Value: aws.String("backflow-agent")},
					{Key: aws.String("backflow"), Value: aws.String("true")},
				},
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

func (m *EC2Manager) TerminateInstance(ctx context.Context, instanceID string) error {
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

func (m *EC2Manager) DescribeInstance(ctx context.Context, instanceID string) (*types.Instance, error) {
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
