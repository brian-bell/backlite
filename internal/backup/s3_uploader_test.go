package backup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS3Uploader_DefaultsCustomEndpointRegionToAuto(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test-access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret-key")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_PROFILE", "")

	emptyAWSFile := filepath.Join(t.TempDir(), "empty-aws-config")
	if err := os.WriteFile(emptyAWSFile, nil, 0o600); err != nil {
		t.Fatalf("WriteFile(empty aws config) error = %v", err)
	}
	t.Setenv("AWS_CONFIG_FILE", emptyAWSFile)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", emptyAWSFile)

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		if r.URL.Path != "/backup-bucket/sqlite/backup.sqlite.gz" {
			t.Fatalf("path = %s, want /backup-bucket/sqlite/backup.sqlite.gz", r.URL.Path)
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Fatalf("read request body: %v", err)
		}
		authorization = r.Header.Get("Authorization")
		w.Header().Set("ETag", `"fake-etag"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	root := t.TempDir()
	artifactPath := filepath.Join(root, "backup.sqlite.gz")
	if err := os.WriteFile(artifactPath, []byte("sqlite backup bytes"), 0o600); err != nil {
		t.Fatalf("WriteFile(artifact) error = %v", err)
	}

	uploader := NewS3Uploader(UploadConfig{
		Bucket:    "backup-bucket",
		Endpoint:  server.URL,
		PathStyle: true,
	})
	result, err := uploader.Upload(context.Background(), UploadInput{
		ArtifactPath: artifactPath,
		Bucket:       "backup-bucket",
		Key:          "sqlite/backup.sqlite.gz",
	})
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if result.ETag != `"fake-etag"` {
		t.Fatalf("ETag = %q, want %q", result.ETag, `"fake-etag"`)
	}
	if !strings.Contains(authorization, "/auto/s3/aws4_request") {
		t.Fatalf("Authorization header = %q, want credential scope to use auto region", authorization)
	}
}
