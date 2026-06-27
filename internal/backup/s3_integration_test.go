//go:build s3integration

package backup_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/brian-bell/backlite/internal/backup"
	"github.com/brian-bell/backlite/internal/debug"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/store"
)

func TestS3BackupUploadEndToEndWithMinIO(t *testing.T) {
	endpoint := os.Getenv("BACKFLOW_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("BACKFLOW_TEST_S3_ENDPOINT is required; run scripts/test-s3-backup.sh")
	}

	accessKey := envOr("BACKFLOW_TEST_S3_ACCESS_KEY", "minioadmin")
	secretKey := envOr("BACKFLOW_TEST_S3_SECRET_KEY", "minioadmin")
	region := envOr("BACKFLOW_TEST_S3_REGION", "us-east-1")

	t.Setenv("AWS_ACCESS_KEY_ID", accessKey)
	t.Setenv("AWS_SECRET_ACCESS_KEY", secretKey)
	t.Setenv("AWS_REGION", region)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatalf("LoadDefaultConfig() error = %v", err)
	}
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})

	bucket := fmt.Sprintf("backlite-s3-test-%d", time.Now().UnixNano())
	createBucketEventually(t, ctx, s3Client, bucket)
	t.Cleanup(func() {
		cleanupBucket(context.Background(), s3Client, bucket)
	})

	root := t.TempDir()
	dbPath := filepath.Join(root, "backlite.db")
	backupDir := filepath.Join(root, "backups")

	sqliteStore, err := store.NewSQLite(ctx, dbPath, filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("NewSQLite() error = %v", err)
	}
	defer sqliteStore.Close()

	now := time.Now().UTC().Truncate(time.Second)
	task := &models.Task{
		ID:        "bf_s3_integration_test",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Branch:    "backlite/s3-integration",
		Prompt:    "exercise s3 backup upload",
		Model:     "claude-sonnet-4-6",
		CreatePR:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := sqliteStore.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask() error = %v", err)
	}

	manager := backup.New(backup.Config{
		Enabled:      true,
		DatabasePath: dbPath,
		Directory:    backupDir,
		Interval:     24 * time.Hour,
		Retention:    7 * 24 * time.Hour,
		Upload: backup.UploadConfig{
			Bucket:    bucket,
			Prefix:    "sqlite/integration/",
			Region:    region,
			Endpoint:  endpoint,
			PathStyle: true,
		},
	})

	manager.MaybeSchedule(ctx)
	status := waitForUploaded(t, manager, 45*time.Second)
	if status.LatestArtifact == nil {
		t.Fatal("LatestArtifact = nil, want uploaded local artifact")
	}

	artifactPath := filepath.Join(backupDir, status.LatestArtifact.FileName)
	metadataPath := artifactPath + ".meta.json"
	markerPath := artifactPath + ".upload.json"

	meta := readMetadataFile(t, metadataPath)
	marker := readUploadMarkerFile(t, markerPath)
	assertMarkerMatchesMetadata(t, marker, meta, bucket, endpoint, "sqlite/integration/"+status.LatestArtifact.FileName)
	assertUploadedObjectMatchesMarker(t, ctx, s3Client, marker)
	validateCompressedBackupContainsTask(t, ctx, artifactPath, task.ID)
	assertDebugStatsReportsUpload(t, manager, marker)

	beforeMarkerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read upload marker before duplicate check: %v", err)
	}
	manager.MaybeSchedule(ctx)
	time.Sleep(500 * time.Millisecond)
	afterMarkerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read upload marker after duplicate check: %v", err)
	}
	if !bytes.Equal(beforeMarkerBytes, afterMarkerBytes) {
		t.Fatal("upload marker changed on a not-due tick, want valid marker to suppress duplicate upload")
	}

	corrupt := marker
	corrupt.SHA256 = "stale-marker-sha"
	writeJSONFile(t, markerPath, corrupt)

	manager.MaybeSchedule(ctx)
	waitFor(t, 45*time.Second, func() bool {
		current := readUploadMarkerFile(t, markerPath)
		return current.SHA256 == meta.SHA256 && current.SizeBytes == meta.SizeBytes && !manager.Status().PendingUpload
	})

	artifacts, err := filepath.Glob(filepath.Join(backupDir, "backlite-*.sqlite.gz"))
	if err != nil {
		t.Fatalf("glob artifacts: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifact count after stale marker reupload = %d, want 1", len(artifacts))
	}
}

func createBucketEventually(t *testing.T, ctx context.Context, client *s3.Client, bucket string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("CreateBucket(%q) did not succeed before timeout: %v", bucket, lastErr)
}

func cleanupBucket(ctx context.Context, client *s3.Client, bucket string) {
	list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err == nil {
		for _, object := range list.Contents {
			if object.Key == nil {
				continue
			}
			_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{
				Bucket: aws.String(bucket),
				Key:    object.Key,
			})
		}
	}
	_, _ = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
}

func waitForUploaded(t *testing.T, manager *backup.Manager, timeout time.Duration) backup.Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var status backup.Status
	for time.Now().Before(deadline) {
		status = manager.Status()
		if status.WorkerState == "idle" && status.LatestArtifact != nil && status.LatestUploaded != nil && !status.PendingUpload {
			return status
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for uploaded backup; last status: %+v", status)
	return backup.Status{}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func readMetadataFile(t *testing.T, path string) backup.Metadata {
	t.Helper()
	var meta backup.Metadata
	readJSONFile(t, path, &meta)
	return meta
}

func readUploadMarkerFile(t *testing.T, path string) backup.UploadMarker {
	t.Helper()
	var marker backup.UploadMarker
	readJSONFile(t, path, &marker)
	return marker
}

func readJSONFile(t *testing.T, path string, dest any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertMarkerMatchesMetadata(t *testing.T, marker backup.UploadMarker, meta backup.Metadata, bucket, endpoint, key string) {
	t.Helper()
	if marker.Bucket != bucket {
		t.Fatalf("marker bucket = %q, want %q", marker.Bucket, bucket)
	}
	if marker.Key != key {
		t.Fatalf("marker key = %q, want %q", marker.Key, key)
	}
	if marker.Endpoint != endpoint {
		t.Fatalf("marker endpoint = %q, want %q", marker.Endpoint, endpoint)
	}
	if marker.SizeBytes != meta.SizeBytes {
		t.Fatalf("marker size_bytes = %d, want %d", marker.SizeBytes, meta.SizeBytes)
	}
	if marker.SHA256 != meta.SHA256 {
		t.Fatalf("marker sha256 = %q, want %q", marker.SHA256, meta.SHA256)
	}
	if marker.UploadedAt.IsZero() {
		t.Fatal("marker uploaded_at is zero")
	}
}

func assertUploadedObjectMatchesMarker(t *testing.T, ctx context.Context, client *s3.Client, marker backup.UploadMarker) {
	t.Helper()
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(marker.Bucket),
		Key:    aws.String(marker.Key),
	})
	if err != nil {
		t.Fatalf("GetObject(%s/%s) error = %v", marker.Bucket, marker.Key, err)
	}
	defer out.Body.Close()

	body, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read uploaded object: %v", err)
	}
	if int64(len(body)) != marker.SizeBytes {
		t.Fatalf("uploaded object size = %d, want marker size %d", len(body), marker.SizeBytes)
	}
}

func validateCompressedBackupContainsTask(t *testing.T, ctx context.Context, artifactPath, taskID string) {
	t.Helper()
	source, err := os.Open(artifactPath)
	if err != nil {
		t.Fatalf("open backup artifact: %v", err)
	}
	defer source.Close()

	reader, err := gzip.NewReader(source)
	if err != nil {
		t.Fatalf("open gzip reader: %v", err)
	}
	defer reader.Close()

	verifyPath := filepath.Join(t.TempDir(), "restore.sqlite")
	destination, err := os.OpenFile(verifyPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create restore sqlite file: %v", err)
	}
	if _, err := io.Copy(destination, reader); err != nil {
		destination.Close()
		t.Fatalf("decompress backup artifact: %v", err)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("close restore sqlite file: %v", err)
	}

	db, err := sql.Open("sqlite", verifyPath)
	if err != nil {
		t.Fatalf("open restored sqlite database: %v", err)
	}
	defer db.Close()

	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("run integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q, want ok", integrity)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE id = ?", taskID).Scan(&count); err != nil {
		t.Fatalf("query restored task: %v", err)
	}
	if count != 1 {
		t.Fatalf("restored task count = %d, want 1", count)
	}
}

func assertDebugStatsReportsUpload(t *testing.T, manager *backup.Manager, marker backup.UploadMarker) {
	t.Helper()
	handler := debug.StatsHandler(func() int { return 0 }, nil, time.Now().Add(-time.Minute), manager.Status)
	req := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/debug/stats status = %d, body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Data struct {
			Backup *struct {
				UploadEnabled  bool                 `json:"upload_enabled"`
				PendingUpload  bool                 `json:"pending_upload"`
				LatestUploaded *backup.UploadMarker `json:"latest_uploaded_artifact"`
			} `json:"backup"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode /debug/stats response: %v", err)
	}
	if resp.Data.Backup == nil {
		t.Fatal("/debug/stats backup field missing")
	}
	if !resp.Data.Backup.UploadEnabled {
		t.Fatal("/debug/stats backup.upload_enabled = false, want true")
	}
	if resp.Data.Backup.PendingUpload {
		t.Fatal("/debug/stats backup.pending_upload = true, want false")
	}
	if resp.Data.Backup.LatestUploaded == nil {
		t.Fatal("/debug/stats backup.latest_uploaded_artifact missing")
	}
	if resp.Data.Backup.LatestUploaded.Key != marker.Key {
		t.Fatalf("/debug/stats latest_uploaded_artifact.key = %q, want %q", resp.Data.Backup.LatestUploaded.Key, marker.Key)
	}
}

func envOr(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
