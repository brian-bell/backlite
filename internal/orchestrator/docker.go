package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
)

type ContainerStatus struct {
	Done       bool
	ExitCode   int
	NeedsInput bool
	Question   string
	Error      string
	LogTail    string
}

type DockerManager struct {
	config    *config.Config
	ssmClient *ssm.Client
}

func NewDockerManager(cfg *config.Config) *DockerManager {
	return &DockerManager{config: cfg}
}

func (m *DockerManager) ensureClient(ctx context.Context) error {
	if m.ssmClient != nil {
		return nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(m.config.AWSRegion))
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	m.ssmClient = ssm.NewFromConfig(cfg)
	return nil
}

func (m *DockerManager) RunAgent(ctx context.Context, instance *models.Instance, task *models.Task) (string, error) {
	if err := m.ensureClient(ctx); err != nil {
		return "", err
	}

	envFlags := []string{
		fmt.Sprintf("-e TASK_ID=%s", task.ID),
		fmt.Sprintf("-e REPO_URL=%s", task.RepoURL),
		fmt.Sprintf("-e BRANCH=%s", task.Branch),
		fmt.Sprintf("-e TARGET_BRANCH=%s", task.TargetBranch),
		fmt.Sprintf("-e PROMPT=%s", shellEscape(task.Prompt)),
		fmt.Sprintf("-e MODEL=%s", task.Model),
		fmt.Sprintf("-e MAX_BUDGET_USD=%g", task.MaxBudgetUSD),
		fmt.Sprintf("-e MAX_TURNS=%d", task.MaxTurns),
		fmt.Sprintf("-e CREATE_PR=%t", task.CreatePR),
	}

	if task.PRTitle != "" {
		envFlags = append(envFlags, fmt.Sprintf("-e PR_TITLE=%s", shellEscape(task.PRTitle)))
	}
	if task.PRBody != "" {
		envFlags = append(envFlags, fmt.Sprintf("-e PR_BODY=%s", shellEscape(task.PRBody)))
	}
	if task.ClaudeMD != "" {
		envFlags = append(envFlags, fmt.Sprintf("-e CLAUDE_MD=%s", shellEscape(task.ClaudeMD)))
	}
	if task.Context != "" {
		envFlags = append(envFlags, fmt.Sprintf("-e TASK_CONTEXT=%s", shellEscape(task.Context)))
	}

	// Auth mode
	envFlags = append(envFlags, fmt.Sprintf("-e AUTH_MODE=%s", string(m.config.AuthMode)))
	if m.config.AuthMode == config.AuthModeAPIKey {
		envFlags = append(envFlags, fmt.Sprintf("-e ANTHROPIC_API_KEY=%s", m.config.AnthropicAPIKey))
	}

	if m.config.GitHubToken != "" {
		envFlags = append(envFlags, fmt.Sprintf("-e GITHUB_TOKEN=%s", m.config.GitHubToken))
	}

	// Custom env vars from task
	for k, v := range task.EnvVars {
		envFlags = append(envFlags, fmt.Sprintf("-e %s=%s", k, shellEscape(v)))
	}

	volumeFlags := ""
	if m.config.AuthMode == config.AuthModeMaxSubscription && m.config.ClaudeCredentialsPath != "" {
		volumeFlags = fmt.Sprintf("-v %s:/home/agent/.claude:ro", m.config.ClaudeCredentialsPath)
	}

	cmd := fmt.Sprintf(
		"docker run -d --cpus=1 --memory=3g %s %s backflow-agent 2>&1 | head -1",
		volumeFlags,
		strings.Join(envFlags, " "),
	)

	output, err := m.runSSMCommand(ctx, instance.InstanceID, cmd)
	if err != nil {
		return "", fmt.Errorf("run container: %w", err)
	}

	containerID := strings.TrimSpace(output)
	if containerID == "" {
		return "", fmt.Errorf("empty container ID returned")
	}

	log.Debug().Str("container", containerID[:12]).Str("instance", instance.InstanceID).Msg("started agent container")
	return containerID, nil
}

func (m *DockerManager) InspectContainer(ctx context.Context, instanceID, containerID string) (ContainerStatus, error) {
	if err := m.ensureClient(ctx); err != nil {
		return ContainerStatus{}, err
	}

	cmd := fmt.Sprintf("docker inspect --format '{{.State.Status}} {{.State.ExitCode}}' %s 2>/dev/null && docker logs --tail 20 %s 2>&1", containerID, containerID)
	output, err := m.runSSMCommand(ctx, instanceID, cmd)
	if err != nil {
		return ContainerStatus{}, err
	}

	lines := strings.SplitN(output, "\n", 2)
	if len(lines) == 0 {
		return ContainerStatus{}, fmt.Errorf("empty inspect output")
	}

	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return ContainerStatus{}, fmt.Errorf("unexpected inspect format: %s", lines[0])
	}

	status := ContainerStatus{}
	if len(lines) > 1 {
		status.LogTail = lines[1]
	}

	dockerStatus := parts[0]
	if dockerStatus == "exited" || dockerStatus == "dead" {
		status.Done = true
		fmt.Sscanf(parts[1], "%d", &status.ExitCode)

		if status.ExitCode != 0 {
			status.Error = fmt.Sprintf("container exited with code %d", status.ExitCode)
		}

		// Check for status.json written by entrypoint
		statusCmd := fmt.Sprintf("docker cp %s:/home/agent/workspace/status.json /dev/stdout 2>/dev/null", containerID)
		if statusJSON, err := m.runSSMCommand(ctx, instanceID, statusCmd); err == nil {
			var agentStatus struct {
				NeedsInput bool   `json:"needs_input"`
				Question   string `json:"question"`
				Complete   bool   `json:"complete"`
				Error      string `json:"error"`
			}
			if json.Unmarshal([]byte(statusJSON), &agentStatus) == nil {
				status.NeedsInput = agentStatus.NeedsInput
				status.Question = agentStatus.Question
				if agentStatus.Error != "" {
					status.Error = agentStatus.Error
				}
			}
		}
	}

	return status, nil
}

func (m *DockerManager) StopContainer(ctx context.Context, instanceID, containerID string) error {
	if err := m.ensureClient(ctx); err != nil {
		return err
	}

	cmd := fmt.Sprintf("docker stop -t 30 %s 2>/dev/null; docker rm %s 2>/dev/null", containerID, containerID)
	_, err := m.runSSMCommand(ctx, instanceID, cmd)
	return err
}

func (m *DockerManager) GetLogs(ctx context.Context, instanceID, containerID string, tail int) (string, error) {
	if err := m.ensureClient(ctx); err != nil {
		return "", err
	}

	cmd := fmt.Sprintf("docker logs --tail %d %s 2>&1", tail, containerID)
	return m.runSSMCommand(ctx, instanceID, cmd)
}

func (m *DockerManager) runSSMCommand(ctx context.Context, instanceID, command string) (string, error) {
	input := &ssm.SendCommandInput{
		InstanceIds:  []string{instanceID},
		DocumentName: aws.String("AWS-RunShellScript"),
		Parameters: map[string][]string{
			"commands": {command},
		},
	}

	result, err := m.ssmClient.SendCommand(ctx, input)
	if err != nil {
		return "", fmt.Errorf("ssm send command: %w", err)
	}

	cmdID := aws.ToString(result.Command.CommandId)

	// Wait for command to complete
	waiter := ssm.NewCommandExecutedWaiter(m.ssmClient)
	getOutput := &ssm.GetCommandInvocationInput{
		CommandId:  aws.String(cmdID),
		InstanceId: aws.String(instanceID),
	}

	if err := waiter.Wait(ctx, getOutput, 60); err != nil {
		return "", fmt.Errorf("wait for command: %w", err)
	}

	out, err := m.ssmClient.GetCommandInvocation(ctx, getOutput)
	if err != nil {
		return "", fmt.Errorf("get command output: %w", err)
	}

	return aws.ToString(out.StandardOutputContent), nil
}

func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
