package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const uploadMarkerExtension = ".upload.json"

type UploadConfig struct {
	Bucket    string
	Prefix    string
	Region    string
	Endpoint  string
	PathStyle bool
}

func (c UploadConfig) Enabled() bool {
	return c.Bucket != ""
}

type Uploader interface {
	Upload(context.Context, UploadInput) (UploadResult, error)
}

type UploadInput struct {
	ArtifactPath string
	Bucket       string
	Key          string
	Endpoint     string
	Metadata     Metadata
}

type UploadResult struct {
	ETag string
}

type UploadMarker struct {
	Bucket     string    `json:"bucket"`
	Key        string    `json:"key"`
	Endpoint   string    `json:"endpoint,omitempty"`
	ETag       string    `json:"etag,omitempty"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	UploadedAt time.Time `json:"uploaded_at"`
}

func uploadMarkerPath(artifactPath string) string {
	return artifactPath + uploadMarkerExtension
}

func uploadMarkerTempPath(artifactPath string) string {
	return uploadMarkerPath(artifactPath) + ".tmp"
}

func objectKey(prefix string, fileName string) string {
	p := strings.Trim(prefix, "/")
	if p == "" {
		return fileName
	}
	return p + "/" + fileName
}

func readUploadMarker(artifactPath string, markerPath string, cfg UploadConfig, meta Metadata) (UploadMarker, bool, error) {
	data, err := os.ReadFile(markerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return UploadMarker{}, false, nil
		}
		return UploadMarker{}, false, fmt.Errorf("read upload marker: %w", err)
	}
	var marker UploadMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return UploadMarker{}, false, nil
	}
	if marker.Bucket == "" || marker.Key == "" || marker.UploadedAt.IsZero() {
		return marker, false, nil
	}
	expectedKey := objectKey(cfg.Prefix, filepath.Base(artifactPath))
	if marker.Bucket != cfg.Bucket || marker.Key != expectedKey || marker.Endpoint != cfg.Endpoint {
		return marker, false, nil
	}
	if marker.SizeBytes != meta.SizeBytes || marker.SHA256 != meta.SHA256 {
		return marker, false, nil
	}
	return marker, true, nil
}

func writeUploadMarker(artifactPath string, marker UploadMarker) error {
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal upload marker: %w", err)
	}
	data = append(data, '\n')
	tmpPath := uploadMarkerTempPath(artifactPath)
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("write upload marker temp: %w", err)
	}
	if err := os.Rename(tmpPath, uploadMarkerPath(artifactPath)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("finalize upload marker: %w", err)
	}
	return nil
}
