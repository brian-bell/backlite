package orchestrator

import "context"

// Writer persists the agent output log and the final task metadata snapshot
// for a completed task.
type Writer interface {
	SaveLog(ctx context.Context, taskID string, logBytes []byte) (string, error)
	SaveMetadata(ctx context.Context, taskID string, metadata any) error
}
