package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite_PersistsShaFile(t *testing.T) {
	dataDir := t.TempDir()
	body := []byte("# Hello\n\nbody text\n")

	sha, path, err := Write(dataDir, body)
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	sum := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(sum[:])
	if sha != wantSHA {
		t.Fatalf("sha mismatch: got %q, want %q", sha, wantSHA)
	}

	wantPath := filepath.Join(dataDir, "ingest", wantSHA+".md")
	if path != wantPath {
		t.Fatalf("path mismatch: got %q, want %q", path, wantPath)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("file bytes mismatch: got %q, want %q", got, body)
	}

	// No tmp debris left in the ingest dir.
	entries, err := os.ReadDir(filepath.Join(dataDir, "ingest"))
	if err != nil {
		t.Fatalf("read ingest dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("found tmp file left behind: %s", e.Name())
		}
	}
}

func TestWrite_IdempotentSecondCall(t *testing.T) {
	dataDir := t.TempDir()
	body := []byte("# repeat\n\nidempotence test\n")

	sha1, path1, err := Write(dataDir, body)
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}

	sha2, path2, err := Write(dataDir, body)
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}

	if sha1 != sha2 {
		t.Fatalf("sha differs across calls: %q vs %q", sha1, sha2)
	}
	if path1 != path2 {
		t.Fatalf("path differs across calls: %q vs %q", path1, path2)
	}

	// File still has correct contents.
	got, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("read after second Write: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("contents corrupted after second Write: got %q", got)
	}

	// No tmp debris.
	entries, err := os.ReadDir(filepath.Join(dataDir, "ingest"))
	if err != nil {
		t.Fatalf("read ingest dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("found tmp file left behind after second Write: %s", e.Name())
		}
	}
}

func TestWrite_RejectsEmpty(t *testing.T) {
	dataDir := t.TempDir()

	for _, body := range [][]byte{nil, {}} {
		_, _, err := Write(dataDir, body)
		if !errors.Is(err, ErrEmptyContent) {
			t.Fatalf("expected ErrEmptyContent for body=%q, got %v", body, err)
		}
	}

	// No ingest dir or its contents created on rejection.
	entries, err := os.ReadDir(filepath.Join(dataDir, "ingest"))
	if err == nil {
		for _, e := range entries {
			t.Fatalf("expected no files on empty rejection, found %s", e.Name())
		}
	}
}
