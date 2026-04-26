package main

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
	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

func TestSetupLogger_StderrOnly(t *testing.T) {
	logger, closer, err := setupLogger("")
	if err != nil {
		t.Fatalf("setupLogger(\"\") returned error: %v", err)
	}
	if closer != nil {
		t.Error("closer should be nil when no log file is specified")
	}
	// Logger should be usable
	logger.Info().Msg("test message")
}

func TestSetupLogger_WithFile(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")

	logger, closer, err := setupLogger(logPath)
	if err != nil {
		t.Fatalf("setupLogger(%q) returned error: %v", logPath, err)
	}
	if closer == nil {
		t.Fatal("closer should not be nil when log file is specified")
	}
	logger.Info().Msg("hello from test")

	// Close to flush, then verify file has content
	closer.Close()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log file: %v", err)
	}
	if len(data) == 0 {
		t.Error("log file is empty, expected content")
	}
}

func TestSetupLogger_CreatesParentDirs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nested", "dir", "test.log")

	_, closer, err := setupLogger(logPath)
	if err != nil {
		t.Fatalf("setupLogger(%q) returned error: %v", logPath, err)
	}
	if closer == nil {
		t.Fatal("closer should not be nil")
	}
	closer.Close()

	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Errorf("log file was not created at %s", logPath)
	}
}

type testStore struct{ store.Store }

// healthCheckStore is a minimal store stub that returns false from HasAPIKeys
// so the auth middleware short-circuits. Other store methods are unused by
// the health/debug-stats routes exercised in these tests.
type healthCheckStore struct{ store.Store }

func (healthCheckStore) HasAPIKeys(context.Context) (bool, error) { return false, nil }
func (healthCheckStore) GetAPIKeyByHash(context.Context, string) (*models.APIKey, error) {
	return nil, store.ErrNotFound
}

type noopLogFetcher struct{}

func (noopLogFetcher) GetLogs(_ context.Context, _ string, _ int) (string, error) {
	return "", nil
}

type noopEmitter struct{}

func (noopEmitter) Emit(notify.Event) {}

func TestBuildHTTPHandler_NoWebhookRoutes(t *testing.T) {
	handler := buildHTTPHandler(
		&config.Config{},
		testStore{},
		nil,
		noopLogFetcher{},
		noopEmitter{},
		func() int { return 0 },
		nil,
		time.Unix(0, 0),
	)

	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{
			name:   "discord webhook remains absent from binary routes",
			method: http.MethodPost,
			path:   "/webhooks/discord",
			want:   http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tc.want {
				t.Fatalf("%s %s: got status %d, want %d", tc.method, tc.path, rr.Code, tc.want)
			}
		})
	}
}

// TestBuildHTTPHandler_HealthEndpointsIsolatedFromBrokenBackupStatus pins
// an architectural invariant: a broken backup status feed (e.g. a panic
// while computing /debug/stats) must not break the health endpoints.
//
// This drives the full handler composition (api.NewServer + /debug/stats)
// with a backupStatusFn that panics, which is the path operators actually
// hit in production.
func TestBuildHTTPHandler_HealthEndpointsIsolatedFromBrokenBackupStatus(t *testing.T) {
	panicCount := 0
	brokenBackupStatus := func() backup.Status {
		panicCount++
		panic("simulated backup status failure")
	}

	handler := buildHTTPHandler(
		&config.Config{},
		healthCheckStore{},
		nil,
		noopLogFetcher{},
		noopEmitter{},
		func() int { return 0 },
		brokenBackupStatus,
		time.Unix(0, 0),
	)

	// First, verify /debug/stats actually invokes the broken function and
	// that chi's Recoverer middleware translates the panic into a 5xx
	// (not a process crash). Without this step the rest of the test is
	// vacuous — we have to prove the failure mode is reachable.
	debugReq := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	debugRR := httptest.NewRecorder()
	handler.ServeHTTP(debugRR, debugReq)
	if debugRR.Code < 500 || debugRR.Code >= 600 {
		t.Fatalf("GET /debug/stats with broken backup status: got %d, want 5xx", debugRR.Code)
	}
	if panicCount == 0 {
		t.Fatal("broken backup status fn was never invoked; cannot prove isolation")
	}

	// Now: the actual invariant. Health endpoints must serve 200.
	for _, path := range []string{"/health", "/api/v1/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200", path, rr.Code)
			continue
		}
		var body struct {
			Data struct {
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
			t.Errorf("GET %s decode body: %v", path, err)
			continue
		}
		if body.Data.Status != "ok" {
			t.Errorf("GET %s data.status = %q, want \"ok\"", path, body.Data.Status)
		}
	}
}
