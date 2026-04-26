package notify

import (
	"time"

	"github.com/brian-bell/backlite/internal/models"
)

// EventOption is a functional option for NewEvent.
type EventOption func(*Event)

// WithReadyForRetry marks the event as ready for user retry (cleanup complete).
func WithReadyForRetry() EventOption {
	return func(e *Event) {
		e.ReadyForRetry = true
	}
}

// WithRetryLimitReached marks the event to indicate the user retry cap has been reached.
func WithRetryLimitReached() EventOption {
	return func(e *Event) {
		e.RetryLimitReached = true
	}
}

// WithContainerStatus sets fields that come from container inspection.
func WithContainerStatus(prURL, message, agentLogTail string) EventOption {
	return func(e *Event) {
		e.PRURL = prURL
		e.Message = message
		e.AgentLogTail = agentLogTail
	}
}

// WithReading sets reading-specific fields on a task completion event.
func WithReading(tldr, noveltyVerdict string, tags []string, connections []models.Connection) EventOption {
	return func(e *Event) {
		e.TLDR = tldr
		e.NoveltyVerdict = noveltyVerdict
		e.Tags = tags
		e.Connections = connections
	}
}

// WithReadingContent attaches the captured content's metadata (its capture
// state and MIME type) to a read-mode task completion event so consumers can
// react to capture-vs-fail without an extra API round-trip.
func WithReadingContent(contentStatus, contentType string) EventOption {
	return func(e *Event) {
		e.ContentStatus = contentStatus
		e.ContentType = contentType
	}
}

// NewEvent constructs an Event from a task, populating core fields.
func NewEvent(eventType EventType, task *models.Task, opts ...EventOption) Event {
	e := Event{
		Type:         eventType,
		TaskID:       task.ID,
		TaskMode:     task.TaskMode,
		ParentTaskID: task.ParentTaskID,
		RepoURL:      task.RepoURL,
		Prompt:       task.Prompt,
		Timestamp:    time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(&e)
	}
	return e
}
