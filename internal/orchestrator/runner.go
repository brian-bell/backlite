package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/backflow-labs/backflow/internal/models"
)

// Runner abstracts container lifecycle management. Implementations include
// DockerManager (EC2/local modes) and FargateManager (Fargate mode).
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
}

// SpotChecker detects spot/preemption interruptions and re-queues affected tasks.
type SpotChecker interface {
	CheckInterruptions(ctx context.Context)
}

// ErrSpotInterruption is returned when an ECS task is stopped due to Fargate
// Spot capacity reclamation.
var ErrSpotInterruption = errors.New("spot interruption")

var errNoCapacity = fmt.Errorf("no instance capacity available")

// IsInstanceGone returns true if the error indicates the EC2 instance no
// longer exists or is not reachable via SSM (e.g. terminated, shutting down),
// or if a Fargate Spot task was interrupted.
func IsInstanceGone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSpotInterruption) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalidinstanceid")
}
