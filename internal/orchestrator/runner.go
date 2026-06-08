package orchestrator

import (
	"context"

	"github.com/brian-bell/backlite/internal/models"
)

// Runner abstracts container lifecycle management on the local Docker host.
type Runner interface {
	RunAgent(ctx context.Context, task *models.Task) (string, error)
	InspectContainer(ctx context.Context, containerID string) (ContainerStatus, error)
	StopContainer(ctx context.Context, containerID string) error
	GetLogs(ctx context.Context, containerID string, tail int) (string, error)
	GetAgentOutput(ctx context.Context, containerID string) (string, error)
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
}
