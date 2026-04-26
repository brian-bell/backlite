package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/store"
)

func TestNewServer_ServesWebAppFallbackWithoutCapturingAPIRoutes(t *testing.T) {
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<!doctype html><title>Backlite Web</title>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.Mkdir(filepath.Join(webDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webDir, "assets", "app.js"), []byte("console.log('backlite')"), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.NewSQLite(context.Background(), dbPath, filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	srv := NewServer(s, &config.Config{WebDir: webDir}, noopLogFetcher{}, noopEmitter{})

	for _, path := range []string{"/", "/readings/bf_READ_ONE"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), "Backlite Web") {
			t.Fatalf("GET %s body = %q, want app index", path, w.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /assets/app.js status = %d, want 200", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "console.log('backlite')" {
		t.Fatalf("asset body = %q", w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health status = %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "Backlite Web") {
		t.Fatal("/api/v1/health returned the web app index")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/missing status = %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "Backlite Web") {
		t.Fatal("/api/v1/missing returned the web app index")
	}
}
