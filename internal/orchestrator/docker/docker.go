package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/orchestrator"
)

// Manager manages agent containers on remote (SSM) or local hosts.
type Manager struct {
	config    *config.Config
	ssmClient *ssm.Client
}

// NewManager creates a new Manager.
func NewManager(cfg *config.Config) *Manager {
	return &Manager{config: cfg}
}

// RunAgent starts a new agent container on the given instance for the task.
// Returns the container ID on success.
func (m *Manager) RunAgent(ctx context.Context, instance *models.Instance, task *models.Task) (string, error) {
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
func (m *Manager) InspectContainer(ctx context.Context, instanceID, containerID string) (orchestrator.ContainerStatus, error) {
	cmd := fmt.Sprintf(
		"docker inspect --format '{{.State.Status}} {{.State.ExitCode}}' %s 2>/dev/null && docker logs --tail 20 %s 2>&1",
		containerID, containerID,
	)
	output, err := m.runCommand(ctx, instanceID, cmd)
	if err != nil {
		return orchestrator.ContainerStatus{}, err
	}

	status, err := parseInspectOutput(output)
	if err != nil {
		return orchestrator.ContainerStatus{}, err
	}

	if status.Done {
		m.enrichFromStatusJSON(ctx, instanceID, containerID, &status)
	}

	return status, nil
}

// StopContainer stops and removes a container.
func (m *Manager) StopContainer(ctx context.Context, instanceID, containerID string) error {
	cmd := fmt.Sprintf("docker stop -t 30 %s 2>/dev/null; docker rm %s 2>/dev/null", containerID, containerID)
	_, err := m.runCommand(ctx, instanceID, cmd)
	return err
}

// GetLogs retrieves the last N lines of a container's logs.
func (m *Manager) GetLogs(ctx context.Context, instanceID, containerID string, tail int) (string, error) {
	cmd := fmt.Sprintf("docker logs --tail %d %s 2>&1", tail, containerID)
	return m.runCommand(ctx, instanceID, cmd)
}

// buildRunCommand constructs the full `docker run` command for an agent task.
func (m *Manager) buildRunCommand(task *models.Task) string {
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
func (m *Manager) buildEnvFlags(task *models.Task) []string {
	flags := []string{
		envFlag("TASK_ID", task.ID),
		envFlag("TASK_MODE", shellEscape(task.TaskMode)),
		envFlag("HARNESS", shellEscape(string(task.Harness))),
		envFlag("REPO_URL", shellEscape(task.RepoURL)),
		envFlag("BRANCH", shellEscape(task.Branch)),
		envFlag("TARGET_BRANCH", shellEscape(task.TargetBranch)),
		envFlag("REVIEW_PR_URL", shellEscape(task.ReviewPRURL)),
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

	if m.config.AuthMode == config.AuthModeAPIKey {
		flags = append(flags, envFlag("ANTHROPIC_API_KEY", m.config.AnthropicAPIKey))
	}
	if m.config.OpenAIAPIKey != "" {
		flags = append(flags, envFlag("OPENAI_API_KEY", m.config.OpenAIAPIKey))
	}
	if m.config.GitHubToken != "" {
		flags = append(flags, envFlag("GITHUB_TOKEN", m.config.GitHubToken))
	}

	for k, v := range task.EnvVars {
		flags = append(flags, envFlag(k, shellEscape(v)))
	}

	return flags
}

// buildVolumeFlags returns the -v flag for mounting Claude credentials when
// using Max subscription auth, or an empty string otherwise.
func (m *Manager) buildVolumeFlags() string {
	if m.config.AuthMode == config.AuthModeMaxSubscription && m.config.ClaudeCredentialsPath != "" {
		return fmt.Sprintf("-v %s:/home/agent/.claude:ro", m.config.ClaudeCredentialsPath)
	}
	return ""
}

// envFlag returns a single "-e KEY=VALUE" flag string.
func envFlag(key, value string) string {
	return fmt.Sprintf("-e %s=%s", key, value)
}

// parseInspectOutput parses the combined output of `docker inspect` +
// `docker logs` into a ContainerStatus.
func parseInspectOutput(output string) (orchestrator.ContainerStatus, error) {
	lines := strings.SplitN(output, "\n", 2)
	if len(lines) == 0 {
		return orchestrator.ContainerStatus{}, fmt.Errorf("empty inspect output")
	}

	parts := strings.Fields(lines[0])
	if len(parts) < 2 {
		return orchestrator.ContainerStatus{}, fmt.Errorf("unexpected inspect format: %s", lines[0])
	}

	status := orchestrator.ContainerStatus{}
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
func (m *Manager) enrichFromStatusJSON(ctx context.Context, instanceID, containerID string, status *orchestrator.ContainerStatus) {
	cmd := fmt.Sprintf("f=$(mktemp) && docker cp %s:/home/agent/workspace/status.json \"$f\" 2>/dev/null && cat \"$f\" && rm -f \"$f\"", containerID)
	statusJSON, err := m.runCommand(ctx, instanceID, cmd)
	if err != nil {
		log.Warn().Err(err).Str("container", containerID[:12]).Msg("failed to read status.json from container")
		return
	}

	var agent orchestrator.AgentStatus
	if err := json.Unmarshal([]byte(statusJSON), &agent); err != nil {
		log.Warn().Err(err).Str("container", containerID[:12]).Str("raw", statusJSON[:min(len(statusJSON), 200)]).Msg("failed to parse status.json")
		return
	}

	log.Debug().Str("container", containerID[:12]).Bool("complete", agent.Complete).Bool("needs_input", agent.NeedsInput).Str("error", agent.Error).Msg("read status.json")

	status.NeedsInput = agent.NeedsInput
	status.Question = agent.Question
	status.Complete = agent.Complete
	status.PRURL = agent.PRURL
	status.CostUSD = agent.CostUSD
	status.ElapsedTimeSec = agent.ElapsedTimeSec
	if agent.Error != "" {
		status.Error = agent.Error
	}
}

// GetAgentOutput extracts the agent's output log from a container via docker cp.
func (m *Manager) GetAgentOutput(ctx context.Context, instanceID, containerID string) (string, error) {
	cmd := fmt.Sprintf("f=$(mktemp) && docker cp %s:/home/agent/workspace/claude_output.log \"$f\" 2>/dev/null && cat \"$f\" && rm -f \"$f\"", containerID)
	return m.runCommand(ctx, instanceID, cmd)
}

// shellEscape wraps a string in single quotes, escaping any embedded single
// quotes so it is safe to interpolate into a shell command.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// isHexString returns true if s is a non-empty string of hex characters (used
// to validate Docker container IDs).
func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
