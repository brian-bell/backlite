package models

import (
	"encoding/json"
	"time"
)

// Reading represents a structured reading result stored in SQLite.
type Reading struct {
	ID             string          `json:"id"`
	TaskID         string          `json:"task_id"`
	URL            string          `json:"url"`
	Title          string          `json:"title"`
	TLDR           string          `json:"tldr"`
	Tags           []string        `json:"tags"`
	Keywords       []string        `json:"keywords"`
	People         []string        `json:"people"`
	Orgs           []string        `json:"orgs"`
	NoveltyVerdict string          `json:"novelty_verdict"`
	Connections    []Connection    `json:"connections"`
	Summary        string          `json:"summary"`
	RawOutput      json.RawMessage `json:"raw_output"`
	Embedding      []float32       `json:"embedding,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`

	// Content-capture fields. Empty string ContentStatus means "no capture
	// attempted" — distinguishes legacy rows (predating the feature) from
	// rows that ran the pipeline and failed.
	ContentType    string     `json:"content_type,omitempty"`
	ContentStatus  string     `json:"content_status"`
	ContentBytes   int64      `json:"content_bytes,omitempty"`
	ExtractedBytes int64      `json:"extracted_bytes,omitempty"`
	ContentSHA256  string     `json:"content_sha256,omitempty"`
	FetchedAt      *time.Time `json:"fetched_at,omitempty"`
}

// Connection links a reading to a related reading with a reason.
type Connection struct {
	ReadingID string `json:"reading_id"`
	Reason    string `json:"reason"`
}
