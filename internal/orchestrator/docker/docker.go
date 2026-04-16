package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/orchestrator"
)

// Manager manages agent containers on remote (SSM) or local hosts.
type Manager struct {
	config     *config.Config
	ssmClient  *ssm.Client
	ssmOnce    sync.Once
	ssmInitErr error
}

// NewManager creates a new Manager.
func NewManager(cfg *config.Config) *Manager {
	return &Manager{config: cfg}
}

// RunAgent starts a new agent container on the given instance for the task.
// Returns the container ID on success.
func (m *Manager) RunAgent(ctx context.Context, instance *models.Instance, task *models.Task) (string, error) {
	secrets := m.buildSecretEnvPairs(task)

	var cmd string
	if m.config.Mode == config.ModeLocal && len(secrets) > 0 {
		envFile, err := writeEnvFile(secrets)
		if err != nil {
			return "", fmt.Errorf("write secret env file: %w", err)
		}
		defer os.Remove(envFile)
		cmd = m.buildRunCommand(task, envFile)
	} else if len(secrets) > 0 {
		dockerCmd := m.buildRunCommand(task, "\"$_ef\"")
		cmd = wrapWithRemoteEnvFile(dockerCmd, secrets)
	} else {
		cmd = m.buildRunCommand(task, "")
	}

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
// envFilePath, if non-empty, adds an --env-file flag for secret env vars.
func (m *Manager) buildRunCommand(task *models.Task, envFilePath string) string {
	envFlags := m.buildEnvFlags(task)
	volumeFlags := m.buildVolumeFlags()
	envFileFlag := ""
	if envFilePath != "" {
		envFileFlag = "--env-file " + envFilePath
	}

	image := task.AgentImage
	if image == "" {
		image = m.config.AgentImage
	}

	return fmt.Sprintf(
		"docker run -d --cpus=%d --memory=%dg %s %s %s %s",
		m.config.ContainerCPUs,
		m.config.ContainerMemGB,
		envFileFlag,
		volumeFlags,
		strings.Join(envFlags, " "),
		image,
	)
}

// buildEnvFlags assembles the -e flags for non-secret env vars. Secret
// credentials (API keys, tokens) are handled separately via --env-file.
func (m *Manager) buildEnvFlags(task *models.Task) []string {
	flags := []string{
		envFlag("TASK_ID", task.ID),
		envFlag("TASK_MODE", shellEscape(task.TaskMode)),
		envFlag("HARNESS", shellEscape(string(task.Harness))),
		envFlag("REPO_URL", shellEscape(task.RepoURL)),
		envFlag("BRANCH", shellEscape(task.Branch)),
		envFlag("TARGET_BRANCH", shellEscape(task.TargetBranch)),
		envFlag("PROMPT", shellEscape(task.Prompt)),
		envFlag("MODEL", shellEscape(task.Model)),
		envFlag("EFFORT", shellEscape(task.Effort)),
		fmt.Sprintf("-e MAX_BUDGET_USD=%g", task.MaxBudgetUSD),
		fmt.Sprintf("-e MAX_TURNS=%d", task.MaxTurns),
		fmt.Sprintf("-e CREATE_PR=%t", task.CreatePR),
		fmt.Sprintf("-e SELF_REVIEW=%t", task.SelfReview),
		fmt.Sprintf("-e FORCE=%t", task.Force),
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

	if task.TaskMode == models.TaskModeRead {
		// SUPABASE_ANON_KEY is a low-privilege publishable JWT (RLS-scoped SELECT only),
		// so it's passed via -e rather than --env-file.
		if m.config.SupabaseURL != "" {
			flags = append(flags, envFlag("SUPABASE_URL", shellEscape(m.config.SupabaseURL)))
		}
		if m.config.SupabaseAnonKey != "" {
			flags = append(flags, envFlag("SUPABASE_ANON_KEY", shellEscape(m.config.SupabaseAnonKey)))
		}
	}

	for k, v := range task.EnvVars {
		flags = append(flags, envFlag(shellEscape(k), shellEscape(v)))
	}

	return flags
}

// buildSecretEnvPairs returns KEY=VALUE pairs for secret credentials, suitable
// for writing to a Docker env file. These are kept out of the docker run
// command line to avoid exposure in process lists and logs.
func (m *Manager) buildSecretEnvPairs(task *models.Task) []string {
	_ = task // reserved for future per-task secrets
	var pairs []string
	if m.config.AnthropicAPIKey != "" {
		pairs = append(pairs, "ANTHROPIC_API_KEY="+m.config.AnthropicAPIKey)
	}
	if m.config.OpenAIAPIKey != "" {
		pairs = append(pairs, "OPENAI_API_KEY="+m.config.OpenAIAPIKey)
	}
	if m.config.GitHubToken != "" {
		pairs = append(pairs, "GITHUB_TOKEN="+m.config.GitHubToken)
	}
	return pairs
}

// writeEnvFile writes KEY=VALUE pairs to a temp file for use with --env-file.
// Returns the file path. Caller must os.Remove the file when done.
// Returns ("", nil) if pairs is empty.
func writeEnvFile(pairs []string) (string, error) {
	if len(pairs) == 0 {
		return "", nil
	}
	f, err := os.CreateTemp("", "backflow-env-*")
	if err != nil {
		return "", err
	}
	defer f.Close()

	for _, p := range pairs {
		if _, err := fmt.Fprintln(f, p); err != nil {
			os.Remove(f.Name())
			return "", err
		}
	}
	return f.Name(), nil
}

// wrapWithRemoteEnvFile wraps a docker run command with temp env file
// creation and cleanup on a remote host (for SSM execution). The secrets
// still appear in the SSM command parameters but are kept off the EC2
// process list and out of docker inspect.
func wrapWithRemoteEnvFile(dockerCmd string, secrets []string) string {
	escaped := make([]string, len(secrets))
	for i, s := range secrets {
		escaped[i] = shellEscape(s)
	}
	return fmt.Sprintf(
		"_ef=$(mktemp) && printf '%%s\\n' %s > \"$_ef\" && %s; _rc=$?; rm -f \"$_ef\"; exit $_rc",
		strings.Join(escaped, " "),
		dockerCmd,
	)
}

// buildVolumeFlags returns volume flags for the docker run command.
// Currently returns empty; reserved for future use.
func (m *Manager) buildVolumeFlags() string {
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
	status.RepoURL = agent.RepoURL
	status.TargetBranch = agent.TargetBranch
	status.TaskMode = agent.TaskMode
	status.URL = agent.URL
	status.Title = agent.Title
	status.TLDR = agent.TLDR
	status.Tags = agent.Tags
	status.Keywords = agent.Keywords
	status.People = agent.People
	status.Orgs = agent.Orgs
	status.NoveltyVerdict = agent.NoveltyVerdict
	status.Connections = agent.Connections
	status.SummaryMarkdown = agent.SummaryMarkdown
	if agent.Error != "" {
		status.Error = agent.Error
	}
}

// GetAgentOutput extracts the agent's output log from a container via docker cp.
func (m *Manager) GetAgentOutput(ctx context.Context, instanceID, containerID string) (string, error) {
	cmd := fmt.Sprintf("f=$(mktemp) && docker cp %s:/home/agent/workspace/container_output.log \"$f\" 2>/dev/null && cat \"$f\" && rm -f \"$f\"", containerID)
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
