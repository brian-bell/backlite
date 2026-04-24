package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEvent_MarshalJSONOmitsLegacyReplyChannel(t *testing.T) {
	event := Event{
		Type:      EventTaskCompleted,
		TaskID:    "bf_123",
		Timestamp: time.Now().UTC(),
	}

	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	jsonBody := string(body)
	if strings.Contains(jsonBody, "reply_channel") {
		t.Fatalf("serialized event still emits reply_channel: %s", jsonBody)
	}
}

func TestWebhookNotifier_NotifyPostsPayload(t *testing.T) {
	var gotBody string
	var gotErr error

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			gotErr = err
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotBody = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	notifier := NewWebhookNotifier(server.URL, nil)
	event := Event{
		Type:      EventTaskCompleted,
		TaskID:    "bf_123",
		Timestamp: time.Now().UTC(),
	}

	if err := notifier.Notify(event); err != nil {
		t.Fatalf("notify: %v", err)
	}
	if gotErr != nil {
		t.Fatalf("read webhook body: %v", gotErr)
	}

	if !strings.Contains(gotBody, `"task_id":"bf_123"`) {
		t.Fatalf("webhook payload missing task_id: %s", gotBody)
	}
	if strings.Contains(gotBody, "reply_channel") {
		t.Fatalf("webhook payload still emits reply_channel: %s", gotBody)
	}
}
