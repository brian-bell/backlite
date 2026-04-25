package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/rs/zerolog/log"
)

type EventType string

const (
	EventTaskCreated     EventType = "task.created"
	EventTaskRunning     EventType = "task.running"
	EventTaskCompleted   EventType = "task.completed"
	EventTaskFailed      EventType = "task.failed"
	EventTaskNeedsInput  EventType = "task.needs_input"
	EventTaskInterrupted EventType = "task.interrupted"
	EventTaskRecovering  EventType = "task.recovering"
	EventTaskCancelled   EventType = "task.cancelled"
	EventTaskRetry       EventType = "task.retry"
)

type Event struct {
	Type              EventType `json:"event"`
	TaskID            string    `json:"task_id"`
	TaskMode          string    `json:"task_mode,omitempty"`
	ParentTaskID      *string   `json:"parent_task_id,omitempty"`
	RepoURL           string    `json:"repo_url,omitempty"`
	Prompt            string    `json:"prompt,omitempty"`
	Message           string    `json:"message,omitempty"`
	PRURL             string    `json:"pr_url,omitempty"`
	AgentLogTail      string    `json:"agent_log_tail,omitempty"`
	ReadyForRetry     bool      `json:"ready_for_retry,omitempty"`
	RetryLimitReached bool      `json:"retry_limit_reached,omitempty"`
	Timestamp         time.Time `json:"timestamp"`

	// Reading-mode fields, populated only for read-task completion events.
	TLDR           string              `json:"tldr,omitempty"`
	NoveltyVerdict string              `json:"novelty_verdict,omitempty"`
	Tags           []string            `json:"tags,omitempty"`
	Connections    []models.Connection `json:"connections,omitempty"`
}

// Emitter emits task lifecycle events.
type Emitter interface {
	Emit(event Event)
}

// Notifier sends notifications for task lifecycle events.
type Notifier interface {
	Notify(event Event) error
}

// ChannelNamer identifies the notification channel for logging.
type ChannelNamer interface {
	Name() string
}

// NoopNotifier discards all events.
type NoopNotifier struct{}

func (NoopNotifier) Notify(Event) error { return nil }
func (NoopNotifier) Name() string       { return "noop" }

// WebhookNotifier sends HTTP POST notifications.
type WebhookNotifier struct {
	url        string
	events     map[EventType]bool // nil = all events
	httpClient *http.Client
}

func NewWebhookNotifier(url string, filterEvents []string) *WebhookNotifier {
	w := &WebhookNotifier{
		url: url,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
	if len(filterEvents) > 0 {
		w.events = make(map[EventType]bool, len(filterEvents))
		for _, e := range filterEvents {
			w.events[EventType(e)] = true
		}
	}
	return w
}

func (w *WebhookNotifier) Notify(event Event) error {
	if w.events != nil && !w.events[event.Type] {
		return nil
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		req, err := http.NewRequest(http.MethodPost, w.url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "backlite-webhook/1.0")

		resp, err := w.httpClient.Do(req)
		if err != nil {
			lastErr = err
			log.Warn().Err(err).Int("attempt", attempt+1).Str("event", string(event.Type)).Msg("webhook request failed")
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Debug().Str("event", string(event.Type)).Str("task_id", event.TaskID).Msg("webhook sent")
			return nil
		}
		lastErr = fmt.Errorf("webhook returned status %d", resp.StatusCode)
		log.Warn().Int("status", resp.StatusCode).Int("attempt", attempt+1).Msg("webhook non-2xx response")
	}

	return fmt.Errorf("webhook failed after 3 attempts: %w", lastErr)
}

func (w *WebhookNotifier) Name() string { return "webhook" }
