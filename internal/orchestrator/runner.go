package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/brian-bell/backlite/internal/models"
)

// Runner abstracts container lifecycle management on the local Docker host.
type Runner interface {
	RunAgent(ctx context.Context, instance *models.Instance, task *models.Task) (string, error)
	InspectContainer(ctx context.Context, instanceID, containerID string) (ContainerStatus, error)
	StopContainer(ctx context.Context, instanceID, containerID string) error
	GetLogs(ctx context.Context, instanceID, containerID string, tail int) (string, error)
	GetAgentOutput(ctx context.Context, instanceID, containerID string) (string, error)
}

// ContainerStatus represents the current state of an agent container.
type ContainerStatus struct {
	Done           bool
	Complete       bool
	ExitCode       int
	NeedsInput     bool
	Question       string
	Error          string
	LogTail        string
	PRURL          string
	CostUSD        float64
	ElapsedTimeSec int
	RepoURL        string
	TargetBranch   string
	TaskMode       string

	// Reading-mode fields (populated only for TaskModeRead).
	URL             string
	Title           string
	TLDR            string
	Tags            []string
	Keywords        []string
	People          []string
	Orgs            []string
	NoveltyVerdict  string
	Connections     []models.Connection
	SummaryMarkdown string
}

// AgentStatus is the JSON structure written by the agent entrypoint to
// /home/agent/workspace/status.json inside the container.
type AgentStatus struct {
	NeedsInput     bool    `json:"needs_input"`
	Question       string  `json:"question"`
	Complete       bool    `json:"complete"`
	Error          string  `json:"error"`
	PRURL          string  `json:"pr_url"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
	ElapsedTimeSec int     `json:"elapsed_time_sec,omitempty"`
	RepoURL        string  `json:"repo_url,omitempty"`
	TargetBranch   string  `json:"target_branch,omitempty"`
	TaskMode       string  `json:"task_mode,omitempty"`

	// Reading-mode fields (populated only for TaskModeRead).
	URL             string              `json:"url,omitempty"`
	Title           string              `json:"title,omitempty"`
	TLDR            string              `json:"tldr,omitempty"`
	Tags            []string            `json:"tags,omitempty"`
	Keywords        []string            `json:"keywords,omitempty"`
	People          []string            `json:"people,omitempty"`
	Orgs            []string            `json:"orgs,omitempty"`
	NoveltyVerdict  string              `json:"novelty_verdict,omitempty"`
	Connections     []models.Connection `json:"connections,omitempty"`
	SummaryMarkdown string              `json:"summary_markdown,omitempty"`
}

var errNoCapacity = fmt.Errorf("no instance capacity available")

// IsInstanceGone returns true if the error indicates the underlying Docker
// daemon is unreachable or the container can no longer be inspected.
func IsInstanceGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalidinstanceid")
}
