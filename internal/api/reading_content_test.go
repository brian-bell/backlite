package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/store"
)

func readingContentTestServer(t *testing.T) (http.Handler, *store.SQLiteStore, string) {
	t.Helper()
	s := newTestStore(t)

	dataDir := t.TempDir()

	cfg := &config.Config{
		AnthropicAPIKey:    "sk-test",
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultCodexModel:  "gpt-5.4",
		DefaultEffort:      "medium",
		DefaultMaxBudget:   10.0,
		DefaultMaxRuntime:  30 * 60e9,
		DefaultMaxTurns:    200,
		MaxUserRetries:     2,
		DataDir:            dataDir,
	}

	return NewServer(s, cfg, noopLogFetcher{}, noopEmitter{}), s, dataDir
}

func seedReadingWithContent(t *testing.T, s *store.SQLiteStore, dataDir, readingID, taskID string, contentStatus, contentType string) {
	t.Helper()
	ctx := context.Background()

	if err := s.CreateTask(ctx, &models.Task{
		ID:       taskID,
		Status:   models.TaskStatusCompleted,
		TaskMode: models.TaskModeRead,
		Prompt:   "https://example.com/x",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	now := time.Now().UTC()
	if err := s.UpsertReading(ctx, &models.Reading{
		ID:             readingID,
		TaskID:         taskID,
		URL:            "https://example.com/" + readingID,
		Title:          "T",
		TLDR:           "tl",
		Tags:           []string{},
		Keywords:       []string{},
		People:         []string{},
		Orgs:           []string{},
		Connections:    []models.Connection{},
		NoveltyVerdict: "new",
		RawOutput:      []byte("{}"),
		CreatedAt:      now,
		ContentType:    contentType,
		ContentStatus:  contentStatus,
		ContentBytes:   42,
		ExtractedBytes: 21,
		ContentSHA256:  "deadbeef",
		FetchedAt:      &now,
	}); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}
}

func writeReadingContentFiles(t *testing.T, dataDir, readingID, raw, extracted string) {
	t.Helper()
	dir := filepath.Join(dataDir, "readings", readingID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.html"), []byte(raw), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extracted.md"), []byte(extracted), 0o644); err != nil {
		t.Fatalf("write extracted: %v", err)
	}
}

func TestGetReadingContent_StreamsExtractedMarkdown(t *testing.T) {
	srv, s, dataDir := readingContentTestServer(t)

	const id = "bf_01HX00000000000000000RDCAP"
	seedReadingWithContent(t, s, dataDir, id, "bf_01HX00000000000000000RDCAT",
		"captured", "text/html; charset=utf-8")
	writeReadingContentFiles(t, dataDir, id, "<html><h1>raw</h1></html>", "# extracted\n\nbody")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+id+"/content", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/markdown; charset=utf-8", got)
	}
	if got := rec.Body.String(); got != "# extracted\n\nbody" {
		t.Errorf("body = %q", got)
	}
}

func TestGetReadingContentRaw_StreamsHTML(t *testing.T) {
	srv, s, dataDir := readingContentTestServer(t)

	const id = "bf_01HX00000000000000000RDRAW"
	seedReadingWithContent(t, s, dataDir, id, "bf_01HX00000000000000000RDRAT",
		"captured", "text/html; charset=utf-8")
	writeReadingContentFiles(t, dataDir, id, "<html><h1>raw</h1></html>", "# extracted")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+id+"/content/raw", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	// Raw endpoint must report the original content type from the row.
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	if got := rec.Body.String(); got != "<html><h1>raw</h1></html>" {
		t.Errorf("body = %q", got)
	}
}

func TestGetReadingContent_404_WhenContentStatusEmpty(t *testing.T) {
	srv, s, dataDir := readingContentTestServer(t)

	const id = "bf_01HX00000000000000000RDLEG"
	// Legacy row: content_status="" — content endpoints must 404 even if a
	// stale file happens to exist.
	seedReadingWithContent(t, s, dataDir, id, "bf_01HX00000000000000000RDLET", "", "")
	writeReadingContentFiles(t, dataDir, id, "stale", "stale")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+id+"/content", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestGetReadingContent_404_WhenReadingMissing(t *testing.T) {
	srv, _, _ := readingContentTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/bf_01HX00000000000000000NOREL/content", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestGetReadingContent_400_RejectsMalformedID(t *testing.T) {
	srv, _, _ := readingContentTestServer(t)

	for _, id := range []string{"..", "bf_..", "bf_short", "../tasks/bf_x"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/readings/"+id+"/content", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("id %q: status = %d, want 400 or 404", id, rec.Code)
		}
	}
}
