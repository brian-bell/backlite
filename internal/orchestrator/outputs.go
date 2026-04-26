package orchestrator

import "context"

// Writer persists the agent output log and the final task metadata snapshot
// for a completed task. For read-mode tasks it also persists the captured
// content artifacts.
type Writer interface {
	SaveLog(ctx context.Context, taskID string, logBytes []byte) (string, error)
	SaveMetadata(ctx context.Context, taskID string, metadata any) error
	SaveReadingContent(ctx context.Context, readingID string, raw, extracted, sidecar []byte) error
}
