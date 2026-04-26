package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/backup"
)

// TestHealthEndpoints_UnaffectedByBrokenBackupConfig pins an architectural
// invariant: the API health endpoints stay green even when the local SQLite
// backup worker is failing. The API has no compile-time link to the backup
// manager today; this regression test guards against future refactors that
// could accidentally couple them.
func TestHealthEndpoints_UnaffectedByBrokenBackupConfig(t *testing.T) {
	// Point the manager at a path that is actually a file — os.MkdirAll
	// inside runBackup will fail, recording an error in the manager's
	// status feed.
	tmpFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(tmpFile, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}

	m := backup.New(backup.Config{
		Enabled:      true,
		DatabasePath: filepath.Join(t.TempDir(), "missing.db"),
		Directory:    tmpFile,
		Interval:     time.Hour,
	})

	m.MaybeSchedule(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status().LastErrorMessage != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if m.Status().LastErrorMessage == "" {
		t.Fatal("backup error was not recorded; cannot prove isolation")
	}

	srv := testServer(t)

	for _, path := range []string{"/health", "/api/v1/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, w.Code)
			continue
		}
		var body struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Errorf("GET %s decode body: %v", path, err)
			continue
		}
		if body.Data.Status != "ok" {
			t.Errorf("GET %s data.status = %q, want %q", path, body.Data.Status, "ok")
		}
	}
}
