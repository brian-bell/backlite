package outputs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func saveArtifacts(t *testing.T, w *FSWriter, taskID string, logBytes []byte, metadata any) {
	t.Helper()
	if _, err := w.SaveLog(context.Background(), taskID, logBytes); err != nil {
		t.Fatalf("SaveLog returned error: %v", err)
	}
	if err := w.SaveMetadata(context.Background(), taskID, metadata); err != nil {
		t.Fatalf("SaveMetadata returned error: %v", err)
	}
}

func TestFSWriter_Save_CreatesDirectory(t *testing.T) {
	root := t.TempDir()

	// The root intentionally starts without a tasks/ subdirectory; Save must
	// create the per-task directory on demand.
	w := New(filepath.Join(root, "fresh"))

	saveArtifacts(t, w, "bf_abc123", []byte("hello"), map[string]string{"id": "bf_abc123"})

	dir := filepath.Join(root, "fresh", "tasks", "bf_abc123")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat task dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
}

func TestFSWriter_Save_WritesBothFiles(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	logContent := []byte("agent log line 1\nagent log line 2\n")
	meta := map[string]any{
		"id":     "bf_meta1",
		"status": "completed",
	}

	saveArtifacts(t, w, "bf_meta1", logContent, meta)

	logPath := filepath.Join(root, "tasks", "bf_meta1", "container_output.log")
	jsonPath := filepath.Join(root, "tasks", "bf_meta1", "task.json")

	gotLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(gotLog) != string(logContent) {
		t.Errorf("log content = %q, want %q", string(gotLog), string(logContent))
	}

	gotJSON, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(gotJSON, &decoded); err != nil {
		t.Fatalf("unmarshal json: %v (body: %s)", err, string(gotJSON))
	}
	if decoded["id"] != "bf_meta1" {
		t.Errorf("metadata id = %v, want %q", decoded["id"], "bf_meta1")
	}
	if decoded["status"] != "completed" {
		t.Errorf("metadata status = %v, want %q", decoded["status"], "completed")
	}
}

func TestFSWriter_Save_Atomic(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	saveArtifacts(t, w, "bf_atomic", []byte("payload"), map[string]string{"k": "v"})

	dir := filepath.Join(root, "tasks", "bf_atomic")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read task dir: %v", err)
	}

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("task dir contains leftover tmp file: %s", e.Name())
		}
	}

	// Expect exactly the two final files.
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("task dir contains %d entries %v, want 2 (container_output.log + task.json)", len(entries), names)
	}
}

func TestFSWriter_Save_Overwrites(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	saveArtifacts(t, w, "bf_over", []byte("first"), map[string]string{"v": "1"})
	saveArtifacts(t, w, "bf_over", []byte("second"), map[string]string{"v": "2"})

	gotLog, err := os.ReadFile(filepath.Join(root, "tasks", "bf_over", "container_output.log"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(gotLog) != "second" {
		t.Errorf("log content = %q, want %q", string(gotLog), "second")
	}

	gotJSON, err := os.ReadFile(filepath.Join(root, "tasks", "bf_over", "task.json"))
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(gotJSON, &decoded); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if decoded["v"] != "2" {
		t.Errorf("metadata v = %q, want %q", decoded["v"], "2")
	}
}

func TestFSWriter_SaveLog_ReturnsURL(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	url, err := w.SaveLog(context.Background(), "bf_url123", []byte("x"))
	if err != nil {
		t.Fatalf("SaveLog returned error: %v", err)
	}
	want := "/api/v1/tasks/bf_url123/output"
	if url != want {
		t.Errorf("SaveLog url = %q, want %q", url, want)
	}
}

func TestFSWriter_SaveReadingContent_WritesAllThreeFiles(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	raw := []byte("<html><body>hello</body></html>")
	extracted := []byte("# hello\n")
	sidecar := []byte(`{"url":"https://example.com","content_status":"captured"}`)

	if err := w.SaveReadingContent(context.Background(), "bf_read1", raw, extracted, sidecar); err != nil {
		t.Fatalf("SaveReadingContent: %v", err)
	}

	dir := filepath.Join(root, "readings", "bf_read1")
	for name, want := range map[string][]byte{
		"raw.html":     raw,
		"extracted.md": extracted,
		"content.json": sidecar,
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}
}

func TestFSWriter_SaveReadingContent_Atomic(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	if err := w.SaveReadingContent(context.Background(), "bf_atomic_r",
		[]byte("raw"), []byte("md"), []byte(`{}`)); err != nil {
		t.Fatalf("SaveReadingContent: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(root, "readings", "bf_atomic_r"))
	if err != nil {
		t.Fatalf("read reading dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("reading dir contains leftover tmp file: %s", e.Name())
		}
	}
	if len(entries) != 3 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("reading dir contains %d entries %v, want 3", len(entries), names)
	}
}

func TestFSWriter_SaveReadingContent_Overwrites(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	if err := w.SaveReadingContent(context.Background(), "bf_over_r",
		[]byte("first-raw"), []byte("first-md"), []byte(`{"v":1}`)); err != nil {
		t.Fatalf("first SaveReadingContent: %v", err)
	}
	if err := w.SaveReadingContent(context.Background(), "bf_over_r",
		[]byte("second-raw"), []byte("second-md"), []byte(`{"v":2}`)); err != nil {
		t.Fatalf("second SaveReadingContent: %v", err)
	}

	dir := filepath.Join(root, "readings", "bf_over_r")
	gotRaw, _ := os.ReadFile(filepath.Join(dir, "raw.html"))
	if string(gotRaw) != "second-raw" {
		t.Errorf("raw.html = %q, want %q", gotRaw, "second-raw")
	}
	gotMD, _ := os.ReadFile(filepath.Join(dir, "extracted.md"))
	if string(gotMD) != "second-md" {
		t.Errorf("extracted.md = %q, want %q", gotMD, "second-md")
	}
	gotSidecar, _ := os.ReadFile(filepath.Join(dir, "content.json"))
	if string(gotSidecar) != `{"v":2}` {
		t.Errorf("content.json = %q, want %q", gotSidecar, `{"v":2}`)
	}
}

func TestFSWriter_SaveReadingContent_SkipsNilExtracted(t *testing.T) {
	// extracted.md is HTML-only — non-HTML payloads pass nil and the file
	// must not appear on disk.
	root := t.TempDir()
	w := New(root)

	if err := w.SaveReadingContent(context.Background(), "bf_no_md",
		[]byte("raw bytes"), nil, []byte(`{"content_type":"application/pdf","content_status":"captured"}`)); err != nil {
		t.Fatalf("SaveReadingContent: %v", err)
	}

	dir := filepath.Join(root, "readings", "bf_no_md")
	if _, err := os.Stat(filepath.Join(dir, "extracted.md")); !os.IsNotExist(err) {
		t.Errorf("extracted.md should not exist, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "raw.pdf")); err != nil {
		t.Errorf("raw.pdf should exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "raw.html")); !os.IsNotExist(err) {
		t.Errorf("raw.html should not exist for PDF capture, stat err = %v", err)
	}
}

func TestFSWriter_SaveReadingContent_RemovesStaleArtifactsOnTypeChange(t *testing.T) {
	root := t.TempDir()
	w := New(root)

	if err := w.SaveReadingContent(context.Background(), "bf_refresh",
		[]byte("<html>old</html>"),
		[]byte("# old"),
		[]byte(`{"content_type":"text/html","content_status":"captured"}`)); err != nil {
		t.Fatalf("first SaveReadingContent: %v", err)
	}
	if err := w.SaveReadingContent(context.Background(), "bf_refresh",
		[]byte("%PDF-1.4\nnew"),
		nil,
		[]byte(`{"content_type":"application/pdf","content_status":"captured"}`)); err != nil {
		t.Fatalf("second SaveReadingContent: %v", err)
	}

	dir := filepath.Join(root, "readings", "bf_refresh")
	if _, err := os.Stat(filepath.Join(dir, "raw.pdf")); err != nil {
		t.Errorf("raw.pdf should exist after PDF refresh: %v", err)
	}
	for _, stale := range []string{"raw.html", "extracted.md"} {
		if _, err := os.Stat(filepath.Join(dir, stale)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed on PDF refresh, stat err = %v", stale, err)
		}
	}
}
