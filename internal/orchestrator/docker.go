package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
)

// ContainerStatus represents the current state of an agent container.
type ContainerStatus struct {
	Done       bool
	ExitCode   int
	NeedsInput bool
	Question   string
	Error      string
	LogTail    string
	PRURL      string
}

// dockerClient is the interface used by the orchestrator to manage containers.
type dockerClient interface {
	RunAgent(ctx context.Context, instance *models.Instance, task *models.Task) (string, error)
	InspectContainer(ctx context.Context, instanceID, containerID string) (ContainerStatus, error)
	StopContainer(ctx context.Context, instanceID, containerID string) error
	GetLogs(ctx context.Context, instanceID, containerID string, tail int) (string, error)
}

// DockerManager manages agent containers on remote (SSM) or local hosts.
type DockerManager struct {
	config    *config.Config
	ssmClient *ssm.Client
}

// NewDockerManager creates a new DockerManager.
func NewDockerManager(cfg *config.Config) *DockerManager {
	return &DockerManager{config: cfg}
}

// RunAgent starts a new agent container on the given instance for the task.
// Returns the container ID on success.
func (m *DockerManager) RunAgent(ctx context.Context, instance *models.Instance, task *models.Task) (string, error) {
	cmd := m.buildRunCommand(task)

	output, err := m.runCommand(ctx, instance.InstanceID, cmd)
	if err != nil {
		return "", fmt.Errorf("run container: %w", err)
	}

	containerID := strings.TrimSpace(output)
	if containerID == "" {
		return "", fmt.Errorf("empty container ID returned")
	}
	if !isHexString(containerID) {
		return "", fmt.Errorf("docker run failed: %s", containerID)
	}

	log.Debug().Str("container", containerID[:12]).Str("instance", instance.InstanceID).Msg("started agent container")
	return containerID, nil
}

// InspectContainer checks a container's status and reads the agent's
// status.json if the container has exited.
func (m *DockerManager) InspectContainer(ctx context.Context, instanceID, containerID string) (ContainerStatus, error) {
	cmd := fmt.Sprintf(
		"docker inspect --format '{{.State.Status}} {{.State.ExitCode}}' %s 2>/dev/null && docker logs --tail 20 %s 2>&1",
		containerID, containerID,
	)
	output, err := m.runCommand(ctx, instanceID, cmd)
	if err != nil {
		return ContainerStatus{}, err
	}

	status, err := parseInspectOutput(output)
	if err != nil {
		return ContainerStatus{}, err
	}

	if status.Done {
		m.enrichFromStatusJSON(ctx, instanceID, containerID, &status)
	}

	return status, nil
}

// StopContainer stops and removes a container.
func (m *DockerManager) StopContainer(ctx context.Context, instanceID, containerID string) error {
	cmd := fmt.Sprintf("docker stop -t 30 %s 2>/dev/null; docker rm %s 2>/dev/null", containerID, containerID)
	_, err := m.runCommand(ctx, instanceID, cmd)
	return err
}

// GetLogs retrieves the last N lines of a container's logs.
func (m *DockerManager) GetLogs(ctx context.Context, instanceID, containerID string, tail int) (string, error) {
	cmd := fmt.Sprintf("docker logs --tail %d %s 2>&1", tail, containerID)
	return m.runCommand(ctx, instanceID, cmd)
}

// buildRunCommand constructs the full `docker run` command for an agent task.
func (m *DockerManager) buildRunCommand(task *models.Task) string {
	envFlags := m.buildEnvFlags(task)
	volumeFlags := m.buildVolumeFlags()

	return fmt.Sprintf(
		"docker run -d --cpus=%d --memory=%dg %s %s backflow-agent",
		m.config.ContainerCPUs,
		m.config.ContainerMemGB,
		volumeFlags,
		strings.Join(envFlags, " "),
	)
}

// buildEnvFlags assembles the -e flags for the docker run command from the
// task configuration and global config.
func (m *DockerManager) buildEnvFlags(task *models.Task) []string {
	flags := []string{
		envFlag("TASK_ID", task.ID),
		envFlag("TASK_MODE", shellEscape(task.TaskMode)),
		envFlag("HARNESS", shellEscape(string(task.Harness))),
		envFlag("REPO_URL", shellEscape(task.RepoURL)),
		envFlag("BRANCH", shellEscape(task.Branch)),
		envFlag("TARGET_BRANCH", shellEscape(task.TargetBranch)),
		fmt.Sprintf("-e REVIEW_PR_NUMBER=%d", task.ReviewPRNumber),
		envFlag("PROMPT", shellEscape(task.Prompt)),
		envFlag("MODEL", shellEscape(task.Model)),
		envFlag("EFFORT", shellEscape(task.Effort)),
		fmt.Sprintf("-e MAX_BUDGET_USD=%g", task.MaxBudgetUSD),
		fmt.Sprintf("-e MAX_TURNS=%d", task.MaxTurns),
		fmt.Sprintf("-e CREATE_PR=%t", task.CreatePR),
		fmt.Sprintf("-e SELF_REVIEW=%t", task.SelfReview),
		envFlag("AUTH_MODE", string(m.config.AuthMode)),
	}

	// Optional task fields
	if task.PRTitle != "" {
		flags = append(flags, envFlag("PR_TITLE", shellEscape(task.PRTitle)))
	}
	if task.PRBody != "" {
		flags = append(flags, envFlag("PR_BODY", shellEscape(task.PRBody)))
	}
	if task.ClaudeMD != "" {
		flags = append(flags, envFlag("CLAUDE_MD", shellEscape(task.ClaudeMD)))
	}
	if task.Context != "" {
		flags = append(flags, envFlag("TASK_CONTEXT", shellEscape(task.Context)))
	}

	// Auth credentials
	if m.config.AuthMode == config.AuthModeAPIKey {
		flags = append(flags, envFlag("ANTHROPIC_API_KEY", m.config.AnthropicAPIKey))
	}
	if m.config.OpenAIAPIKey != "" {
		flags = append(flags, envFlag("OPENAI_API_KEY", m.config.OpenAIAPIKey))
	}
	if m.config.GitHubToken != "" {
		flags = append(flags, envFlag("GITHUB_TOKEN", m.config.GitHubToken))
	}

	// Custom env vars from the task
	for k, v := range task.EnvVars {
		flags = append(flags, envFlag(k, shellEscape(v)))
	}

	return flags
}

// buildVolumeFlags returns the -v flag for mounting Claude credentials when
// using Max subscription auth, or an empty string otherwise.
func (m *DockerManager) buildVolumeFlags() string {
	if m.config.AuthMode == config.AuthModeMaxSubscription && m.config.ClaudeCredentialsPath != "" {
		return fmt.Sprintf("-v %s:/home/agent/.claude:ro", m.config.ClaudeCredentialsPath)
	}
	return ""
}

// envFlag returns a single "-e KEY=VALUE" flag string.
func envFlag(key, value string) string {
	return fmt.Sprintf("-e %s=%s", key, value)
}

// agentStatus is the JSON structure written by the agent entrypoint to
// /home/agent/workspace/status.json inside the container.
type agentStatus struct {
	NeedsInput bool   `json:"needs_input"`
	Question   string `json:"question"`
	Complete   bool   `json:"complete"`
	Error      string `json:"error"`
	PRURL      string `json:"pr_url"`
}

// parseInspectOutput parses the combined output of `docker inspect` +
// `docker logs` into a ContainerStatus.
func parseInspectOutput(output string) (ContainerStatus, error) {
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

	dockerState := parts[0]
	if dockerState == "exited" || dockerState == "dead" {
		status.Done = true
		fmt.Sscanf(parts[1], "%d", &status.ExitCode)
		if status.ExitCode != 0 {
			status.Error = fmt.Sprintf("container exited with code %d", status.ExitCode)
		}
	}

	return status, nil
}

// enrichFromStatusJSON reads the agent's status.json from the container and
// merges its fields into the ContainerStatus.
func (m *DockerManager) enrichFromStatusJSON(ctx context.Context, instanceID, containerID string, status *ContainerStatus) {
	cmd := fmt.Sprintf("docker cp %s:/home/agent/workspace/status.json /dev/stdout 2>/dev/null", containerID)
	statusJSON, err := m.runCommand(ctx, instanceID, cmd)
	if err != nil {
		return
	}

	var agent agentStatus
	if json.Unmarshal([]byte(statusJSON), &agent) != nil {
		return
	}

	status.NeedsInput = agent.NeedsInput
	status.Question = agent.Question
	status.PRURL = agent.PRURL
	if agent.Error != "" {
		status.Error = agent.Error
	}
}
