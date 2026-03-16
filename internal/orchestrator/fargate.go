package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cloudwatchlogstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
)

const backflowStatusLogPrefix = "BACKFLOW_STATUS_JSON:"

// errSpotInterruption is returned when an ECS task is stopped due to Fargate
// Spot capacity reclamation. It is checked via errors.Is in isInstanceGone.
var errSpotInterruption = errors.New("spot interruption")

// FargateManager manages agent containers as standalone ECS tasks.
type FargateManager struct {
	config *config.Config
	s3     s3Client
	ecs    *ecs.Client
	cwLogs *cloudwatchlogs.Client
}

func NewFargateManager(cfg *config.Config, s3 s3Client) *FargateManager {
	return &FargateManager{config: cfg, s3: s3}
}

func (m *FargateManager) ensureClients(ctx context.Context) error {
	if m.ecs != nil && m.cwLogs != nil {
		return nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(m.config.AWSRegion))
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}

	if m.ecs == nil {
		m.ecs = ecs.NewFromConfig(cfg)
	}
	if m.cwLogs == nil {
		m.cwLogs = cloudwatchlogs.NewFromConfig(cfg)
	}
	return nil
}

func (m *FargateManager) RunAgent(ctx context.Context, _ *models.Instance, task *models.Task) (string, error) {
	if err := m.ensureClients(ctx); err != nil {
		return "", err
	}

	containerCPU := int32(m.taskCPUUnits())
	containerMemory := int32(m.taskMemoryMiB())

	assignPublicIP := ecstypes.AssignPublicIpDisabled
	if m.config.ECSAssignPublicIP {
		assignPublicIP = ecstypes.AssignPublicIpEnabled
	}

	awsvpc := &ecstypes.AwsVpcConfiguration{
		Subnets:        append([]string(nil), m.config.ECSSubnets...),
		AssignPublicIp: assignPublicIP,
	}
	if len(m.config.ECSSecurityGroups) > 0 {
		awsvpc.SecurityGroups = append([]string(nil), m.config.ECSSecurityGroups...)
	}

	envVars := m.buildECSEnvVars(task)
	if m.s3 != nil {
		var err error
		envVars, err = m.offloadLargeEnvVars(ctx, task.ID, envVars)
		if err != nil {
			return "", fmt.Errorf("offload env vars to S3: %w", err)
		}
	}

	input := &ecs.RunTaskInput{
		Cluster:        aws.String(m.config.ECSCluster),
		TaskDefinition: aws.String(m.config.ECSTaskDefinition),
		Count:          aws.Int32(1),
		ClientToken:    aws.String(task.ID),
		StartedBy:      aws.String(task.ID),
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: awsvpc,
		},
		Overrides: &ecstypes.TaskOverride{
			Cpu:    aws.String(strconv.Itoa(m.taskCPUUnits())),
			Memory: aws.String(strconv.Itoa(m.taskMemoryMiB())),
			ContainerOverrides: []ecstypes.ContainerOverride{
				{
					Name:        aws.String(m.containerName()),
					Environment: envVars,
					Cpu:         aws.Int32(containerCPU),
					Memory:      aws.Int32(containerMemory),
				},
			},
		},
	}

	if m.launchType() == "FARGATE_SPOT" {
		input.CapacityProviderStrategy = []ecstypes.CapacityProviderStrategyItem{
			{
				CapacityProvider: aws.String("FARGATE_SPOT"),
				Weight:           1,
			},
			{
				CapacityProvider: aws.String("FARGATE"),
				Weight:           0,
			},
		}
	} else {
		input.LaunchType = ecstypes.LaunchTypeFargate
	}

	output, err := m.ecs.RunTask(ctx, input)
	if err != nil {
		return "", fmt.Errorf("run ecs task: %w", err)
	}
	if len(output.Failures) > 0 {
		return "", fmt.Errorf("run ecs task failed: %s", formatECSFailure(output.Failures[0]))
	}
	if len(output.Tasks) == 0 {
		return "", fmt.Errorf("run ecs task returned no tasks")
	}

	taskARN := aws.ToString(output.Tasks[0].TaskArn)
	if taskARN == "" {
		return "", fmt.Errorf("run ecs task returned empty task ARN")
	}

	log.Debug().Str("task_id", task.ID).Str("task_arn", taskARN).Msg("started fargate task")
	return taskARN, nil
}

func (m *FargateManager) InspectContainer(ctx context.Context, _, containerID string) (ContainerStatus, error) {
	if err := m.ensureClients(ctx); err != nil {
		return ContainerStatus{}, err
	}

	output, err := m.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(m.config.ECSCluster),
		Tasks:   []string{containerID},
	})
	if err != nil {
		return ContainerStatus{}, fmt.Errorf("describe ecs task: %w", err)
	}
	if len(output.Failures) > 0 {
		return ContainerStatus{}, fmt.Errorf("describe ecs task failed: %s", formatECSFailure(output.Failures[0]))
	}
	if len(output.Tasks) == 0 {
		return ContainerStatus{}, fmt.Errorf("ecs task %s not found", containerID)
	}

	task := output.Tasks[0]
	if isSpotInterruptionReason(aws.ToString(task.StoppedReason)) {
		return ContainerStatus{}, fmt.Errorf("%w: %s", errSpotInterruption, aws.ToString(task.StoppedReason))
	}

	status, err := mapECSTaskStatus(task, m.containerName())
	if err != nil {
		return ContainerStatus{}, err
	}

	if status.Done {
		events, err := m.getLogEvents(ctx, containerID, 200)
		if err != nil {
			log.Debug().Err(err).Str("task_arn", containerID).Msg("failed to fetch fargate logs for completed task")
			return status, nil
		}
		status.LogTail = formatLogEvents(events)
		if agent, ok := parseStatusFromLogEvents(events); ok {
			status.Complete = agent.Complete
			status.NeedsInput = agent.NeedsInput
			status.Question = agent.Question
			status.PRURL = agent.PRURL
			status.CostUSD = agent.CostUSD
			status.ElapsedTimeSec = agent.ElapsedTimeSec
			if agent.Error != "" {
				status.Error = agent.Error
			}
		}
	}

	return status, nil
}

func (m *FargateManager) StopContainer(ctx context.Context, _, containerID string) error {
	if err := m.ensureClients(ctx); err != nil {
		return err
	}

	_, err := m.ecs.StopTask(ctx, &ecs.StopTaskInput{
		Cluster: aws.String(m.config.ECSCluster),
		Task:    aws.String(containerID),
		Reason:  aws.String("stopped by backflow"),
	})
	if err != nil {
		return fmt.Errorf("stop ecs task %s: %w", containerID, err)
	}
	return nil
}

func (m *FargateManager) GetLogs(ctx context.Context, _, containerID string, tail int) (string, error) {
	if err := m.ensureClients(ctx); err != nil {
		return "", err
	}

	events, err := m.getLogEvents(ctx, containerID, tail)
	if err != nil {
		return "", err
	}
	return formatLogEvents(events), nil
}

func (m *FargateManager) buildECSEnvVars(task *models.Task) []ecstypes.KeyValuePair {
	vars := []ecstypes.KeyValuePair{
		ecsEnvVar("TASK_ID", task.ID),
		ecsEnvVar("TASK_MODE", task.TaskMode),
		ecsEnvVar("HARNESS", string(task.Harness)),
		ecsEnvVar("REPO_URL", task.RepoURL),
		ecsEnvVar("BRANCH", task.Branch),
		ecsEnvVar("TARGET_BRANCH", task.TargetBranch),
		ecsEnvVar("REVIEW_PR_URL", task.ReviewPRURL),
		ecsEnvVar("REVIEW_PR_NUMBER", strconv.Itoa(task.ReviewPRNumber)),
		ecsEnvVar("PROMPT", task.Prompt),
		ecsEnvVar("MODEL", task.Model),
		ecsEnvVar("EFFORT", task.Effort),
		ecsEnvVar("MAX_BUDGET_USD", strconv.FormatFloat(task.MaxBudgetUSD, 'g', -1, 64)),
		ecsEnvVar("MAX_TURNS", strconv.Itoa(task.MaxTurns)),
		ecsEnvVar("CREATE_PR", strconv.FormatBool(task.CreatePR)),
		ecsEnvVar("SELF_REVIEW", strconv.FormatBool(task.SelfReview)),
		ecsEnvVar("AUTH_MODE", string(m.config.AuthMode)),
	}

	if task.PRTitle != "" {
		vars = append(vars, ecsEnvVar("PR_TITLE", task.PRTitle))
	}
	if task.PRBody != "" {
		vars = append(vars, ecsEnvVar("PR_BODY", task.PRBody))
	}
	if task.ClaudeMD != "" {
		vars = append(vars, ecsEnvVar("CLAUDE_MD", task.ClaudeMD))
	}
	if task.Context != "" {
		vars = append(vars, ecsEnvVar("TASK_CONTEXT", task.Context))
	}

	if m.config.AuthMode == config.AuthModeAPIKey {
		vars = append(vars, ecsEnvVar("ANTHROPIC_API_KEY", m.config.AnthropicAPIKey))
	}
	if m.config.OpenAIAPIKey != "" {
		vars = append(vars, ecsEnvVar("OPENAI_API_KEY", m.config.OpenAIAPIKey))
	}
	if m.config.GitHubToken != "" {
		vars = append(vars, ecsEnvVar("GITHUB_TOKEN", m.config.GitHubToken))
	}

	keys := make([]string, 0, len(task.EnvVars))
	for key := range task.EnvVars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		vars = append(vars, ecsEnvVar(key, task.EnvVars[key]))
	}

	return vars
}

func (m *FargateManager) getLogEvents(ctx context.Context, containerID string, tail int) ([]cloudwatchlogstypes.OutputLogEvent, error) {
	if tail <= 0 {
		tail = 100
	}

	streamName, err := m.buildLogStreamName(containerID)
	if err != nil {
		return nil, err
	}

	output, err := m.cwLogs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(m.config.CloudWatchLogGroup),
		LogStreamName: aws.String(streamName),
		Limit:         aws.Int32(int32(tail)),
		StartFromHead: aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("get CloudWatch logs for %s: %w", streamName, err)
	}
	return output.Events, nil
}

func (m *FargateManager) buildLogStreamName(taskARN string) (string, error) {
	taskID, err := taskIDFromARN(taskARN)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%s", m.logStreamPrefix(), m.containerName(), taskID), nil
}

func (m *FargateManager) containerName() string {
	if m.config.ECSContainerName != "" {
		return m.config.ECSContainerName
	}
	return "backflow-agent"
}

func (m *FargateManager) logStreamPrefix() string {
	if m.config.ECSLogStreamPrefix != "" {
		return m.config.ECSLogStreamPrefix
	}
	return "ecs"
}

func (m *FargateManager) launchType() string {
	if m.config.ECSLaunchType != "" {
		return m.config.ECSLaunchType
	}
	return "FARGATE_SPOT"
}

func (m *FargateManager) taskCPUUnits() int {
	return m.config.ContainerCPUs * 1024
}

func (m *FargateManager) taskMemoryMiB() int {
	return m.config.ContainerMemGB * 1024
}

// ecsOverridesTarget is the target maximum size for ECS container overrides.
// The hard ECS limit is 8192 bytes; we leave headroom for JSON structure.
const ecsOverridesTarget = 7000

// offloadableEnvVars maps env var names to their S3 URL counterparts.
// These are the fields most likely to be large (prompt, context, etc.).
var offloadableEnvVars = map[string]string{
	"PROMPT":       "PROMPT_S3_URL",
	"CLAUDE_MD":    "CLAUDE_MD_S3_URL",
	"TASK_CONTEXT": "TASK_CONTEXT_S3_URL",
	"PR_BODY":      "PR_BODY_S3_URL",
}

// offloadLargeEnvVars uploads large env var values to S3 and replaces them
// with pre-signed GET URLs so the entrypoint can download them. This avoids
// the ECS RunTask 8192-byte container overrides limit.
func (m *FargateManager) offloadLargeEnvVars(ctx context.Context, taskID string, vars []ecstypes.KeyValuePair) ([]ecstypes.KeyValuePair, error) {
	size := estimateOverridesSize(vars)
	if size <= ecsOverridesTarget {
		return vars, nil
	}

	// Find offloadable vars, sorted largest first.
	type candidate struct {
		index int
		key   string
		size  int
	}
	var candidates []candidate
	for i, v := range vars {
		key := aws.ToString(v.Name)
		if _, ok := offloadableEnvVars[key]; ok {
			candidates = append(candidates, candidate{i, key, len(aws.ToString(v.Value))})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].size > candidates[j].size })

	for _, c := range candidates {
		if size <= ecsOverridesTarget {
			break
		}

		value := aws.ToString(vars[c.index].Value)
		if len(value) == 0 {
			continue
		}

		s3Key := fmt.Sprintf("task-config/%s/%s", taskID, strings.ToLower(c.key))
		if _, err := m.s3.Upload(ctx, s3Key, []byte(value)); err != nil {
			return nil, fmt.Errorf("upload %s to S3: %w", c.key, err)
		}
		url, err := m.s3.PresignGetURL(ctx, s3Key, 1*time.Hour)
		if err != nil {
			return nil, fmt.Errorf("presign %s URL: %w", c.key, err)
		}

		urlKey := offloadableEnvVars[c.key]
		oldSize := len(c.key) + len(value)
		vars[c.index] = ecsEnvVar(urlKey, url)
		newSize := len(urlKey) + len(url)
		size -= oldSize - newSize

		log.Debug().Str("task_id", taskID).Str("field", c.key).Int("original_bytes", len(value)).Msg("offloaded env var to S3")
	}

	return vars, nil
}

// estimateOverridesSize returns a rough byte count for the ECS container
// overrides JSON. Each env var contributes its key, value, and ~30 bytes
// of JSON structure ({"name":"…","value":"…"}).
func estimateOverridesSize(vars []ecstypes.KeyValuePair) int {
	size := 200 // base overhead for task override + container override structure
	for _, v := range vars {
		size += len(aws.ToString(v.Name)) + len(aws.ToString(v.Value)) + 30
	}
	return size
}

func ecsEnvVar(key, value string) ecstypes.KeyValuePair {
	return ecstypes.KeyValuePair{
		Name:  aws.String(key),
		Value: aws.String(value),
	}
}

func formatECSFailure(failure ecstypes.Failure) string {
	parts := make([]string, 0, 3)
	if arn := aws.ToString(failure.Arn); arn != "" {
		parts = append(parts, arn)
	}
	if reason := aws.ToString(failure.Reason); reason != "" {
		parts = append(parts, reason)
	}
	if detail := aws.ToString(failure.Detail); detail != "" {
		parts = append(parts, detail)
	}
	if len(parts) == 0 {
		return "unknown ecs failure"
	}
	return strings.Join(parts, ": ")
}

func mapECSTaskStatus(task ecstypes.Task, containerName string) (ContainerStatus, error) {
	state := aws.ToString(task.LastStatus)
	status := ContainerStatus{}

	switch state {
	case "PROVISIONING", "PENDING", "ACTIVATING", "RUNNING":
		return status, nil
	case "DEACTIVATING", "STOPPING", "DEPROVISIONING", "STOPPED":
		status.Done = true
	default:
		return ContainerStatus{}, fmt.Errorf("unknown ECS task status: %s", state)
	}

	container := findECSContainer(task.Containers, containerName)
	if container != nil && container.ExitCode != nil {
		status.ExitCode = int(aws.ToInt32(container.ExitCode))
	}

	if status.ExitCode != 0 {
		status.Error = fmt.Sprintf("container exited with code %d", status.ExitCode)
	} else if reason := aws.ToString(task.StoppedReason); reason != "" {
		status.Error = reason
	}

	return status, nil
}

func findECSContainer(containers []ecstypes.Container, containerName string) *ecstypes.Container {
	for i := range containers {
		if aws.ToString(containers[i].Name) == containerName {
			return &containers[i]
		}
	}
	if len(containers) == 0 {
		return nil
	}
	return &containers[0]
}

func parseStatusFromLogEvents(events []cloudwatchlogstypes.OutputLogEvent) (agentStatus, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		message := strings.TrimSpace(aws.ToString(events[i].Message))
		if !strings.HasPrefix(message, backflowStatusLogPrefix) {
			continue
		}

		var status agentStatus
		payload := strings.TrimSpace(strings.TrimPrefix(message, backflowStatusLogPrefix))
		if payload == "" {
			return agentStatus{}, false
		}
		if err := json.Unmarshal([]byte(payload), &status); err != nil {
			return agentStatus{}, false
		}
		return status, true
	}
	return agentStatus{}, false
}

func formatLogEvents(events []cloudwatchlogstypes.OutputLogEvent) string {
	lines := make([]string, 0, len(events))
	for _, event := range events {
		lines = append(lines, aws.ToString(event.Message))
	}
	return strings.Join(lines, "\n")
}

func taskIDFromARN(taskARN string) (string, error) {
	parts := strings.Split(strings.TrimSpace(taskARN), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid ECS task ARN: %s", taskARN)
	}

	taskID := strings.TrimSpace(parts[len(parts)-1])
	if taskID == "" {
		return "", fmt.Errorf("invalid ECS task ARN: %s", taskARN)
	}
	return taskID, nil
}

func isSpotInterruptionReason(reason string) bool {
	r := strings.ToLower(reason)
	return strings.Contains(r, "host ec2 (spot) terminated") ||
		strings.Contains(r, "spot capacity")
}
