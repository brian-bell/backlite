//go:build !nocontainers

package blackbox_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// WebhookEvent mirrors the JSON structure of notify.Event as sent by the
// WebhookNotifier. Defined here (rather than imported) to keep the black-box
// test package free of internal dependencies — this also serves as a contract
// test for the webhook payload format.
type WebhookEvent struct {
	Event         string    `json:"event"`
	TaskID        string    `json:"task_id"`
	RepoURL       string    `json:"repo_url,omitempty"`
	Prompt        string    `json:"prompt,omitempty"`
	Message       string    `json:"message,omitempty"`
	PRURL         string    `json:"pr_url,omitempty"`
	AgentLogTail  string    `json:"agent_log_tail,omitempty"`
	ReplyChannel  string    `json:"reply_channel,omitempty"`
	ReadyForRetry bool      `json:"ready_for_retry,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
}

// WebhookListener is an httptest.Server that captures webhook events and
// supports configurable response behavior per test.
type WebhookListener struct {
	server     *httptest.Server
	mu         sync.Mutex
	events     []WebhookEvent
	statusCode int
	latency    time.Duration
}

// newWebhookListener creates and starts a new WebhookListener. The caller must
// call Close() when done.
func newWebhookListener() *WebhookListener {
	wl := &WebhookListener{
		statusCode: http.StatusOK,
	}

	wl.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wl.mu.Lock()
		latency := wl.latency
		code := wl.statusCode
		wl.mu.Unlock()

		if latency > 0 {
			time.Sleep(latency)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}

		var event WebhookEvent
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "json error", http.StatusBadRequest)
			return
		}

		wl.mu.Lock()
		wl.events = append(wl.events, event)
		wl.mu.Unlock()

		w.WriteHeader(code)
	}))

	return wl
}

// URL returns the listener's base URL for use in BACKFLOW_WEBHOOK_URL.
func (wl *WebhookListener) URL() string {
	return wl.server.URL
}

// SetBehavior configures the HTTP status code and response latency for
// subsequent webhook deliveries.
func (wl *WebhookListener) SetBehavior(statusCode int, latency time.Duration) {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	wl.statusCode = statusCode
	wl.latency = latency
}

// EventsForTask returns a copy of all events received for the given task ID.
func (wl *WebhookListener) EventsForTask(taskID string) []WebhookEvent {
	wl.mu.Lock()
	defer wl.mu.Unlock()

	var filtered []WebhookEvent
	for _, e := range wl.events {
		if e.TaskID == taskID {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// AllEvents returns a copy of all received events.
func (wl *WebhookListener) AllEvents() []WebhookEvent {
	wl.mu.Lock()
	defer wl.mu.Unlock()

	out := make([]WebhookEvent, len(wl.events))
	copy(out, wl.events)
	return out
}

// WaitForEventType polls until an event with the given type appears for the
// specified task, or fatals after timeout.
func (wl *WebhookListener) WaitForEventType(t *testing.T, taskID, eventType string, timeout time.Duration) WebhookEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range wl.EventsForTask(taskID) {
			if e.Event == eventType {
				return e
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for webhook event %q for task %s (waited %s)", eventType, taskID, timeout)
	return WebhookEvent{} // unreachable
}

// Reset clears all captured events and restores default behavior (200 OK, no latency).
func (wl *WebhookListener) Reset() {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	wl.events = nil
	wl.statusCode = http.StatusOK
	wl.latency = 0
}

// Close shuts down the underlying httptest.Server.
func (wl *WebhookListener) Close() {
	wl.server.Close()
}
