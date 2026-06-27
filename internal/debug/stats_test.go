package debug

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/backup"
	"github.com/brian-bell/backlite/internal/store"
)

type mockPoolStatter struct{}

func (mockPoolStatter) PoolStats() store.PoolStats {
	return store.PoolStats{
		AcquiredConns: 2,
		IdleConns:     3,
		TotalConns:    5,
		MaxConns:      10,
	}
}

func TestStatsHandler_ReturnsExpectedFields(t *testing.T) {
	startedAt := time.Now().Add(-10 * time.Second)
	handler := StatsHandler(func() int { return 3 }, mockPoolStatter{}, startedAt, nil)

	req := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Orchestrator struct {
				RunningTasks int `json:"running_tasks"`
			} `json:"orchestrator"`
			Pool struct {
				AcquiredConns int32 `json:"acquired_conns"`
				IdleConns     int32 `json:"idle_conns"`
				TotalConns    int32 `json:"total_conns"`
				MaxConns      int32 `json:"max_conns"`
			} `json:"pool"`
			UptimeSeconds float64 `json:"uptime_seconds"`
			Runtime       struct {
				HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
				SysBytes       uint64 `json:"sys_bytes"`
			} `json:"runtime"`
			PID int `json:"pid"`
		} `json:"data"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Data.Orchestrator.RunningTasks != 3 {
		t.Errorf("running_tasks = %d, want 3", resp.Data.Orchestrator.RunningTasks)
	}
	if resp.Data.Pool.AcquiredConns != 2 {
		t.Errorf("acquired_conns = %d, want 2", resp.Data.Pool.AcquiredConns)
	}
	if resp.Data.Pool.MaxConns != 10 {
		t.Errorf("max_conns = %d, want 10", resp.Data.Pool.MaxConns)
	}
	if resp.Data.UptimeSeconds < 10 {
		t.Errorf("uptime_seconds = %f, want >= 10", resp.Data.UptimeSeconds)
	}
	if resp.Data.PID == 0 {
		t.Error("pid = 0, want non-zero")
	}
	if resp.Data.Runtime.SysBytes == 0 {
		t.Error("sys_bytes = 0, want non-zero")
	}
}

func TestStatsHandler_IncludesBackupStatus(t *testing.T) {
	finalized := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	uploadedAt := finalized.Add(2 * time.Minute)
	nextUploadAttemptAt := finalized.Add(5 * time.Minute)
	statusFn := func() backup.Status {
		return backup.Status{
			Enabled:             true,
			Directory:           "/var/backups/backlite",
			Interval:            24 * time.Hour,
			Retention:           7 * 24 * time.Hour,
			UploadEnabled:       true,
			UploadBucket:        "backlite-prod",
			UploadPrefix:        "sqlite/daily/",
			UploadEndpoint:      "https://s3.example.test",
			PendingUpload:       true,
			NextUploadAttemptAt: &nextUploadAttemptAt,
			WorkerState:         "idle",
			LatestArtifact: &backup.Metadata{
				FileName:    "backlite-20260425T120000Z.sqlite.gz",
				FinalizedAt: finalized,
				SHA256:      "abc",
				SizeBytes:   123,
			},
			LatestUploaded: &backup.UploadMarker{
				Bucket:     "backlite-prod",
				Key:        "sqlite/daily/backlite-20260425T120000Z.sqlite.gz",
				Endpoint:   "https://s3.example.test",
				ETag:       `"abc123"`,
				SHA256:     "abc",
				SizeBytes:  123,
				UploadedAt: uploadedAt,
			},
			RecentErrors: []backup.ErrorEntry{{
				At:      finalized.Add(time.Minute),
				Phase:   "upload",
				Message: "temporary s3 outage",
			}},
		}
	}

	handler := StatsHandler(func() int { return 0 }, nil, time.Now(), statusFn)

	req := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Backup *struct {
				Enabled             bool       `json:"enabled"`
				Directory           string     `json:"directory"`
				UploadEnabled       bool       `json:"upload_enabled"`
				UploadBucket        string     `json:"upload_bucket"`
				UploadPrefix        string     `json:"upload_prefix"`
				UploadEndpoint      string     `json:"upload_endpoint"`
				PendingUpload       bool       `json:"pending_upload"`
				NextUploadAttemptAt *time.Time `json:"next_upload_attempt_at"`
				WorkerState         string     `json:"worker_state"`
				LatestArtifact      *struct {
					FileName  string `json:"file_name"`
					SizeBytes int64  `json:"size_bytes"`
				} `json:"latest_artifact"`
				LatestUploaded *struct {
					Bucket     string    `json:"bucket"`
					Key        string    `json:"key"`
					Endpoint   string    `json:"endpoint"`
					ETag       string    `json:"etag"`
					UploadedAt time.Time `json:"uploaded_at"`
				} `json:"latest_uploaded_artifact"`
				RecentErrors []struct {
					Phase   string `json:"phase"`
					Message string `json:"message"`
				} `json:"recent_errors"`
			} `json:"backup"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Backup == nil {
		t.Fatal("data.backup missing from response")
	}
	if !resp.Data.Backup.Enabled {
		t.Errorf("backup.enabled = false, want true")
	}
	if resp.Data.Backup.Directory != "/var/backups/backlite" {
		t.Errorf("backup.directory = %q, want /var/backups/backlite", resp.Data.Backup.Directory)
	}
	if resp.Data.Backup.WorkerState != "idle" {
		t.Errorf("backup.worker_state = %q, want idle", resp.Data.Backup.WorkerState)
	}
	if !resp.Data.Backup.UploadEnabled {
		t.Error("backup.upload_enabled = false, want true")
	}
	if resp.Data.Backup.UploadBucket != "backlite-prod" {
		t.Errorf("backup.upload_bucket = %q, want backlite-prod", resp.Data.Backup.UploadBucket)
	}
	if resp.Data.Backup.UploadPrefix != "sqlite/daily/" {
		t.Errorf("backup.upload_prefix = %q, want sqlite/daily/", resp.Data.Backup.UploadPrefix)
	}
	if resp.Data.Backup.UploadEndpoint != "https://s3.example.test" {
		t.Errorf("backup.upload_endpoint = %q", resp.Data.Backup.UploadEndpoint)
	}
	if !resp.Data.Backup.PendingUpload {
		t.Error("backup.pending_upload = false, want true")
	}
	if resp.Data.Backup.NextUploadAttemptAt == nil || !resp.Data.Backup.NextUploadAttemptAt.Equal(nextUploadAttemptAt) {
		t.Errorf("backup.next_upload_attempt_at = %v, want %v", resp.Data.Backup.NextUploadAttemptAt, nextUploadAttemptAt)
	}
	if resp.Data.Backup.LatestArtifact == nil {
		t.Fatal("backup.latest_artifact missing")
	}
	if resp.Data.Backup.LatestArtifact.FileName != "backlite-20260425T120000Z.sqlite.gz" {
		t.Errorf("latest_artifact.file_name = %q", resp.Data.Backup.LatestArtifact.FileName)
	}
	if resp.Data.Backup.LatestArtifact.SizeBytes != 123 {
		t.Errorf("latest_artifact.size_bytes = %d, want 123", resp.Data.Backup.LatestArtifact.SizeBytes)
	}
	if resp.Data.Backup.LatestUploaded == nil {
		t.Fatal("backup.latest_uploaded_artifact missing")
	}
	if resp.Data.Backup.LatestUploaded.Key != "sqlite/daily/backlite-20260425T120000Z.sqlite.gz" {
		t.Errorf("latest_uploaded_artifact.key = %q", resp.Data.Backup.LatestUploaded.Key)
	}
	if resp.Data.Backup.LatestUploaded.ETag != `"abc123"` {
		t.Errorf("latest_uploaded_artifact.etag = %q", resp.Data.Backup.LatestUploaded.ETag)
	}
	if len(resp.Data.Backup.RecentErrors) != 1 {
		t.Fatalf("len(backup.recent_errors) = %d, want 1", len(resp.Data.Backup.RecentErrors))
	}
	if resp.Data.Backup.RecentErrors[0].Phase != "upload" {
		t.Errorf("recent_errors[0].phase = %q, want upload", resp.Data.Backup.RecentErrors[0].Phase)
	}
}

func TestStatsHandler_NilPoolStatter(t *testing.T) {
	handler := StatsHandler(func() int { return 0 }, nil, time.Now(), nil)

	req := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Pool struct {
				MaxConns int32 `json:"max_conns"`
			} `json:"pool"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.Pool.MaxConns != 0 {
		t.Errorf("max_conns = %d, want 0 when no pool statter", resp.Data.Pool.MaxConns)
	}
}
