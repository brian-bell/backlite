package orchestrator

import "context"

// Writer persists agent output and task metadata for a completed task and
// returns a URL under which the API serves the bytes back to callers.
type Writer interface {
	Save(ctx context.Context, taskID string, logBytes []byte, metadata any) (string, error)
}
