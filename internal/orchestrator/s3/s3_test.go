package s3_test

import (
	"context"
	"testing"

	"github.com/backflow-labs/backflow/internal/config"
	orchestrator "github.com/backflow-labs/backflow/internal/orchestrator"
	s3pkg "github.com/backflow-labs/backflow/internal/orchestrator/s3"
)

// Compile-time check: *Uploader must satisfy orchestrator.S3Client.
var _ orchestrator.S3Client = (*s3pkg.Uploader)(nil)

func TestNewUploader_NilWhenNoBucket(t *testing.T) {
	cfg := &config.Config{S3Bucket: ""}
	uploader, err := s3pkg.NewUploader(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uploader != nil {
		t.Fatal("expected nil uploader when S3Bucket is empty")
	}
}
