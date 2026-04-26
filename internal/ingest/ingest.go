// Package ingest persists user-supplied content to the data dir under a
// content-addressed path. It is the entry point for markdown bodies that
// arrive via the API's inline_content field on read-mode tasks; once a body
// is on disk at <DataDir>/ingest/<sha>.md the orchestrator can bind-mount
// the file into a reader container without exposing the rest of the data
// directory.
package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrEmptyContent is returned when Write is called with a nil or empty body.
var ErrEmptyContent = errors.New("ingest: content is empty")

// Write computes the SHA-256 of body, atomically writes it to
// <dataDir>/ingest/<sha>.md, and returns the hex SHA and absolute path.
//
// The write is atomic: bytes go to a *.tmp sibling first and are renamed
// into place. The caller is responsible for ensuring dataDir exists or is
// creatable; this function will MkdirAll the ingest subdirectory.
func Write(dataDir string, body []byte) (sha string, path string, err error) {
	if len(body) == 0 {
		return "", "", ErrEmptyContent
	}
	sum := sha256.Sum256(body)
	sha = hex.EncodeToString(sum[:])

	dir := filepath.Join(dataDir, "ingest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("ingest: mkdir: %w", err)
	}

	path = filepath.Join(dir, sha+".md")

	tmp, err := os.CreateTemp(dir, sha+".*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("ingest: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", "", fmt.Errorf("ingest: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", "", fmt.Errorf("ingest: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return "", "", fmt.Errorf("ingest: rename: %w", err)
	}
	return sha, path, nil
}
