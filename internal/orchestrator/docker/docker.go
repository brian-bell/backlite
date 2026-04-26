package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/orchestrator"
)

// runCmdFn runs a bash command and returns its stdout (or an error). Manager
// holds it as a field so tests can swap in a fake without invoking real bash.
type runCmdFn func(ctx context.Context, command string) (string, error)

// Manager manages agent containers on the local Docker host.
type Manager struct {
	config *config.Config
	runCmd runCmdFn
}

// NewManager creates a new Manager.
func NewManager(cfg *config.Config) *Manager {
	m := &Manager{config: cfg}
	m.runCmd = m.runCommand
	return m
}

// newManagerWithRunner builds a Manager whose command runner is the supplied
// function instead of the real bash exec. Test-only.
func newManagerWithRunner(run runCmdFn) *Manager {
	return &Manager{config: &config.Config{}, runCmd: run}
}

// RunAgent starts a new agent container on the local Docker host for the task.
// Returns the container ID on success.
func (m *Manager) RunAgent(ctx context.Context, task *models.Task) (string, error) {
	secrets := m.buildSecretEnvPairs(task)

	var cmd string
	if len(secrets) > 0 {
		envFile, err := writeEnvFile(secrets)
		if err != nil {
			return "", fmt.Errorf("write secret env file: %w", err)
		}
		defer os.Remove(envFile)
		cmd = m.buildRunCommand(task, envFile)
	} else {
		cmd = m.buildRunCommand(task, "")
	}

	output, err := m.runCommand(ctx, cmd)
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

	log.Debug().Str("container", containerID[:12]).Msg("started agent container")
	return containerID, nil
}

// InspectContainer checks a container's status and reads the agent's
// status.json if the container has exited.
func (m *Manager) InspectContainer(ctx context.Context, containerID string) (orchestrator.ContainerStatus, error) {
	cmd := fmt.Sprintf(
		"docker inspect --format '{{.State.Status}} {{.State.ExitCode}}' %s 2>/dev/null && docker logs --tail 20 %s 2>&1",
		containerID, containerID,
	)
	output, err := m.runCommand(ctx, cmd)
	if err != nil {
		return orchestrator.ContainerStatus{}, err
	}

	status, err := parseInspectOutput(output)
	if err != nil {
		return orchestrator.ContainerStatus{}, err
	}

	if status.Done {
		m.enrichFromStatusJSON(ctx, containerID, &status)
	}

	return status, nil
}

// StopContainer stops and removes a container.
func (m *Manager) StopContainer(ctx context.Context, containerID string) error {
	cmd := fmt.Sprintf("docker stop -t 30 %s 2>/dev/null; docker rm %s 2>/dev/null", containerID, containerID)
	_, err := m.runCommand(ctx, cmd)
	return err
}

// GetLogs retrieves the last N lines of a container's logs.
func (m *Manager) GetLogs(ctx context.Context, containerID string, tail int) (string, error) {
	cmd := fmt.Sprintf("docker logs --tail %d %s 2>&1", tail, containerID)
	return m.runCommand(ctx, cmd)
}

// buildRunCommand constructs the full `docker run` command for an agent task.
// envFilePath, if non-empty, adds an --env-file flag for secret env vars.
func (m *Manager) buildRunCommand(task *models.Task, envFilePath string) string {
	envFlags := m.buildEnvFlags(task)
	volumeFlags := m.buildVolumeFlags()
	networkFlags := m.buildNetworkFlags(task)
	envFileFlag := ""
	if envFilePath != "" {
		envFileFlag = "--env-file " + envFilePath
	}

	image := task.AgentImage
	if image == "" {
		image = m.config.AgentImage
	}

	return fmt.Sprintf(
		"docker run -d --cpus=%d --memory=%dg %s %s %s %s %s",
		m.config.ContainerCPUs,
		m.config.ContainerMemGB,
		envFileFlag,
		volumeFlags,
		networkFlags,
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
		flags = append(flags, envFlag("BACKFLOW_API_BASE_URL", shellEscape(m.internalAPIBaseURL())))
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
	if m.config.APIKey != "" {
		pairs = append(pairs, "BACKFLOW_API_KEY="+m.config.APIKey)
	}
	if m.config.ResendAPIKey != "" {
		pairs = append(pairs, "RESEND_API_KEY="+m.config.ResendAPIKey)
	}
	if m.config.NotifyEmailFrom != "" {
		pairs = append(pairs, "NOTIFY_EMAIL_FROM="+m.config.NotifyEmailFrom)
	}
	if m.config.NotifyEmailTo != "" {
		pairs = append(pairs, "NOTIFY_EMAIL_TO="+m.config.NotifyEmailTo)
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
	f, err := os.CreateTemp("", "backlite-env-*")
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

// buildVolumeFlags returns volume flags for the docker run command.
// Currently returns empty; reserved for future use.
func (m *Manager) buildVolumeFlags() string {
	return ""
}

func (m *Manager) buildNetworkFlags(task *models.Task) string {
	if task.TaskMode != models.TaskModeRead {
		return ""
	}
	return "--add-host host.docker.internal:host-gateway"
}

func (m *Manager) internalAPIBaseURL() string {
	if m.config.InternalAPIBaseURL != "" {
		return m.config.InternalAPIBaseURL
	}
	host, port := splitListenAddr(m.config.ListenAddr)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "localhost" || host == "127.0.0.1" {
		host = "host.docker.internal"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func splitListenAddr(addr string) (string, string) {
	if addr == "" {
		return "host.docker.internal", "8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "", strings.TrimPrefix(addr, ":")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "host.docker.internal", "8080"
	}
	return host, port
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
func (m *Manager) enrichFromStatusJSON(ctx context.Context, containerID string, status *orchestrator.ContainerStatus) {
	cmd := fmt.Sprintf("f=$(mktemp) && docker cp %s:/home/agent/workspace/status.json \"$f\" 2>/dev/null && cat \"$f\" && rm -f \"$f\"", containerID)
	statusJSON, err := m.runCommand(ctx, cmd)
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
// The agent writes this log to /tmp (outside the git workspace) so the agent's
// own git operations cannot clobber it.
func (m *Manager) GetAgentOutput(ctx context.Context, containerID string) (string, error) {
	cmd := fmt.Sprintf("f=$(mktemp) && docker cp %s:/tmp/container_output.log \"$f\" && cat \"$f\" && rm -f \"$f\"", containerID)
	return m.runCmd(ctx, cmd)
}

// GetReadingContent extracts the captured reading artifacts written by the
// reader container's pre-fetch + extraction step. Missing files yield nil byte
// slices (no error) so callers can record content_status accordingly.
func (m *Manager) GetReadingContent(ctx context.Context, containerID string) (raw, extracted, sidecar []byte, err error) {
	raw = m.copyReadingFile(ctx, containerID, "/home/agent/workspace/raw.html")
	extracted = m.copyReadingFile(ctx, containerID, "/home/agent/workspace/extracted.md")
	sidecar = m.copyReadingFile(ctx, containerID, "/home/agent/workspace/content.json")
	return raw, extracted, sidecar, nil
}

// copyReadingFile pulls a single file out of the container via docker cp.
// Missing files surface as nil — the docker cp invocation tolerates the
// absence with `2>/dev/null` and the fallback `cat` of an empty/missing path.
func (m *Manager) copyReadingFile(ctx context.Context, containerID, srcPath string) []byte {
	cmd := fmt.Sprintf(
		"f=$(mktemp) && docker cp %s:%s \"$f\" 2>/dev/null && cat \"$f\" && rm -f \"$f\"",
		containerID, srcPath,
	)
	out, err := m.runCmd(ctx, cmd)
	if err != nil {
		return nil
	}
	if out == "" {
		return nil
	}
	return []byte(out)
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
