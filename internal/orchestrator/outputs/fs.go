// Package outputs writes agent output artifacts (stdout log + task metadata)
// to the local filesystem and returns a stable URL under which the API serves
// the files back to callers.
package outputs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FSWriter persists agent output to {Root}/tasks/{taskID}/. The two files it
// writes are container_output.log (raw stdout) and task.json (JSON metadata).
// Writes are atomic per file via a *.tmp sibling + os.Rename, so readers never
// observe a half-written file.
type FSWriter struct {
	Root string
}

// New returns an FSWriter rooted at root. Directories are created lazily on
// the first Save for a given task.
func New(root string) *FSWriter {
	return &FSWriter{Root: root}
}

// SaveLog writes the raw agent log for taskID and returns the API-relative URL
// that serves it back to callers.
func (w *FSWriter) SaveLog(_ context.Context, taskID string, logBytes []byte) (string, error) {
	dir := filepath.Join(w.Root, "tasks", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("outputs: mkdir task dir: %w", err)
	}

	if err := writeAtomic(filepath.Join(dir, "container_output.log"), logBytes); err != nil {
		return "", err
	}

	return "/api/v1/tasks/" + taskID + "/output", nil
}

// SaveMetadata writes the JSON task metadata snapshot (task.json) for taskID.
func (w *FSWriter) SaveMetadata(_ context.Context, taskID string, metadata any) error {
	dir := filepath.Join(w.Root, "tasks", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("outputs: mkdir task dir: %w", err)
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("outputs: marshal metadata: %w", err)
	}
	if err := writeAtomic(filepath.Join(dir, "task.json"), data); err != nil {
		return err
	}

	return nil
}

// SaveReadingContent persists the captured artifacts for readingID under
// {Root}/readings/{readingID}/. The HTML happy path supplies all three byte
// slices (raw HTML, extracted markdown, sidecar JSON). For non-HTML payloads
// extracted may be nil, in which case extracted.md is skipped — raw.html and
// content.json are still required.
func (w *FSWriter) SaveReadingContent(_ context.Context, readingID string, raw, extracted, sidecar []byte) error {
	dir := filepath.Join(w.Root, "readings", readingID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("outputs: mkdir reading dir: %w", err)
	}
	if err := writeAtomic(filepath.Join(dir, "raw.html"), raw); err != nil {
		return err
	}
	if extracted != nil {
		if err := writeAtomic(filepath.Join(dir, "extracted.md"), extracted); err != nil {
			return err
		}
	}
	if err := writeAtomic(filepath.Join(dir, "content.json"), sidecar); err != nil {
		return err
	}
	return nil
}

func writeAtomic(finalPath string, data []byte) error {
	tmp := finalPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("outputs: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("outputs: rename %s: %w", finalPath, err)
	}
	return nil
}
