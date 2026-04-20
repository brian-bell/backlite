package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
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

type noopLogFetcher struct{}

func (noopLogFetcher) GetLogs(_ context.Context, _, _ string, _ int) (string, error) {
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
		{
			name:   "sms webhook remains absent from binary routes",
			method: http.MethodPost,
			path:   "/webhooks/sms/inbound",
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
