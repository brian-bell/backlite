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
