package orchestrator

import (
	"context"
	"time"
)

// S3Client is the interface used by the orchestrator and Fargate manager to
// upload data to S3.
type S3Client interface {
	Upload(ctx context.Context, key string, data []byte) (string, error)
	UploadJSON(ctx context.Context, key string, data []byte) (string, error)
	PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error)
}
