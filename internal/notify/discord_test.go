package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/discord"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

type inMemoryDiscordThreadStore struct {
	mu      sync.Mutex
	threads map[string]*models.DiscordTaskThread
}

func (s *inMemoryDiscordThreadStore) GetDiscordTaskThread(_ context.Context, taskID string) (*models.DiscordTaskThread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads == nil {
		s.threads = make(map[string]*models.DiscordTaskThread)
	}
	thread, ok := s.threads[taskID]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *thread
	return &cp, nil
}

func (s *inMemoryDiscordThreadStore) UpsertDiscordTaskThread(_ context.Context, thread *models.DiscordTaskThread) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.threads == nil {
		s.threads = make(map[string]*models.DiscordTaskThread)
	}
	cp := *thread
	s.threads[thread.TaskID] = &cp
	return nil
}

func (s *inMemoryDiscordThreadStore) get(taskID string) (*models.DiscordTaskThread, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.threads[taskID]
	if !ok {
		return nil, false
	}
	cp := *thread
	return &cp, true
}

func TestDiscordNotifier_BootstrapsThreadOnFirstEvent(t *testing.T) {
	store := &inMemoryDiscordThreadStore{}

	var mu sync.Mutex
	var got []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path)
		mu.Unlock()

		if r.Header.Get("Authorization") != "Bot bot-token" {
			t.Fatalf("Authorization = %q, want Bot bot-token", r.Header.Get("Authorization"))
		}

		switch r.URL.Path {
		case "/channels/channel-123/messages":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			assertDiscordPayload(t, body, "Task created", "Task bf_task1 was created.")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "msg-1"})
		case "/channels/channel-123/messages/msg-1/threads":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			var payload discord.StartThreadPayload
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("unmarshal thread payload: %v", err)
			}
			if payload.Name != "backflow-bf_task1" {
				t.Fatalf("thread name = %q, want %q", payload.Name, "backflow-bf_task1")
			}
			if payload.AutoArchiveDuration != 10080 {
				t.Fatalf("auto_archive_duration = %d, want 10080", payload.AutoArchiveDuration)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "thread-1"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := discord.NewClientWithBaseURL(srv.URL, "bot-token")
	notifier := NewDiscordNotifier(client, store, "channel-123", nil)

	event := Event{
		Type:      EventTaskCreated,
		TaskID:    "bf_task1",
		RepoURL:   "https://github.com/org/repo",
		Prompt:    "fix the bug",
		Timestamp: time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC),
	}
	if err := notifier.Notify(event); err != nil {
		t.Fatalf("Notify = %v", err)
	}

	thread, ok := store.get("bf_task1")
	if !ok {
		t.Fatal("expected task thread mapping to be stored")
	}
	if thread.RootMessageID != "msg-1" {
		t.Fatalf("RootMessageID = %q, want %q", thread.RootMessageID, "msg-1")
	}
	if thread.ThreadID != "thread-1" {
		t.Fatalf("ThreadID = %q, want %q", thread.ThreadID, "thread-1")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("request count = %d, want 2", len(got))
	}
	if got[0] != "POST /channels/channel-123/messages" {
		t.Fatalf("first request = %q, want create-message request", got[0])
	}
	if got[1] != "POST /channels/channel-123/messages/msg-1/threads" {
		t.Fatalf("second request = %q, want thread-create request", got[1])
	}
}

func TestDiscordNotifier_UsesStoredThreadForLaterEvents(t *testing.T) {
	store := &inMemoryDiscordThreadStore{
		threads: map[string]*models.DiscordTaskThread{
			"bf_task1": {
				TaskID:        "bf_task1",
				RootMessageID: "msg-1",
				ThreadID:      "thread-1",
				CreatedAt:     time.Now().UTC(),
				UpdatedAt:     time.Now().UTC(),
			},
		},
	}

	var mu sync.Mutex
	var got []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, _ := io.ReadAll(r.Body)

		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch r.URL.Path {
		case "/channels/thread-1/messages":
			assertDiscordPayload(t, body, "Task running", "Task bf_task1 is now running.")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "msg-2"})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := discord.NewClientWithBaseURL(srv.URL, "bot-token")
	notifier := NewDiscordNotifier(client, store, "channel-123", nil)

	if err := notifier.Notify(Event{
		Type:      EventTaskRunning,
		TaskID:    "bf_task1",
		Timestamp: time.Date(2026, 3, 21, 12, 5, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Notify = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("request count = %d, want 1", len(got))
	}
	if got[0] != "POST /channels/thread-1/messages" {
		t.Fatalf("request = %q, want thread message post", got[0])
	}
}

func TestDiscordNotifier_SwallowsDeliveryFailures(t *testing.T) {
	store := &inMemoryDiscordThreadStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	client := discord.NewClientWithBaseURL(srv.URL, "bot-token")
	notifier := NewDiscordNotifier(client, store, "channel-123", nil)

	if err := notifier.Notify(Event{
		Type:      EventTaskCreated,
		TaskID:    "bf_task_fail",
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Notify = %v, want nil", err)
	}

	if _, ok := store.get("bf_task_fail"); ok {
		t.Fatal("task thread mapping should not be stored after delivery failure")
	}
}

func TestDiscordNotifier_FiltersEvents(t *testing.T) {
	notifier := NewDiscordNotifier(nil, nil, "", []string{"task.completed", "task.failed"})

	if err := notifier.Notify(Event{Type: EventTaskCompleted, TaskID: "bf_TEST001", Timestamp: time.Now()}); err != nil {
		t.Fatalf("Notify(completed) = %v, want nil", err)
	}
	if err := notifier.Notify(Event{Type: EventTaskRunning, TaskID: "bf_TEST001", Timestamp: time.Now()}); err != nil {
		t.Fatalf("Notify(running) = %v, want nil", err)
	}
}

func TestDiscordNotifier_AllEvents(t *testing.T) {
	notifier := NewDiscordNotifier(nil, nil, "", nil)
	for _, et := range []EventType{EventTaskCreated, EventTaskRunning, EventTaskCompleted, EventTaskFailed, EventTaskInterrupted, EventTaskRecovering, EventTaskCancelled, EventTaskRetry} {
		if err := notifier.Notify(Event{Type: et, TaskID: "bf_TEST001", Timestamp: time.Now()}); err != nil {
			t.Fatalf("Notify(%s) = %v, want nil", et, err)
		}
	}
}

func TestDiscordNotifier_Name(t *testing.T) {
	notifier := NewDiscordNotifier(nil, nil, "", nil)
	if got := notifier.Name(); got != "discord" {
		t.Fatalf("Name() = %q, want %q", got, "discord")
	}
}

func TestDiscordEmbedFormatting(t *testing.T) {
	t.Run("completed includes PR URL", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:      EventTaskCompleted,
			TaskID:    "bf_1",
			PRURL:     "https://github.com/org/repo/pull/42",
			Message:   "all good",
			Timestamp: time.Now().UTC(),
		})

		if embed.URL != "https://github.com/org/repo/pull/42" {
			t.Fatalf("URL = %q, want PR URL", embed.URL)
		}
		if !strings.Contains(embed.Description, "completed") {
			t.Fatalf("Description = %q, want completed status", embed.Description)
		}
	})

	t.Run("failed includes summary and log tail", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:         EventTaskFailed,
			TaskID:       "bf_1",
			Message:      "container exited 1",
			AgentLogTail: strings.Repeat("log ", 400),
			Timestamp:    time.Now().UTC(),
		})

		if !containsField(embed.Fields, "Failure", "container exited 1") {
			t.Fatalf("embed fields = %#v, want failure summary", embed.Fields)
		}
		if !containsField(embed.Fields, "Log Tail", "") {
			t.Fatalf("embed fields = %#v, want log tail field", embed.Fields)
		}
		if len(embed.Fields) < 2 {
			t.Fatalf("embed fields = %#v, want at least 2 fields", embed.Fields)
		}
	})

	t.Run("cancelled pending cleanup", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:      EventTaskCancelled,
			TaskID:    "bf_1",
			Timestamp: time.Now().UTC(),
		})
		if len(embed.Fields) != 1 {
			t.Fatalf("embed fields = %#v, want 1 field", embed.Fields)
		}
		if !strings.Contains(embed.Description, "Stopping container") {
			t.Fatalf("description = %q, want cancellation-in-progress message", embed.Description)
		}
	})

	t.Run("cancelled ready for retry", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:          EventTaskCancelled,
			TaskID:        "bf_1",
			ReadyForRetry: true,
			Timestamp:     time.Now().UTC(),
		})
		if !strings.Contains(embed.Description, "ready to retry") {
			t.Fatalf("description = %q, want ready-to-retry message", embed.Description)
		}
	})

	t.Run("reading completed new verdict full embed", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:           EventTaskCompleted,
			TaskID:         "bf_r1",
			TaskMode:       "read",
			Prompt:         "https://example.com/article",
			TLDR:           "Article explains how transformers work",
			NoveltyVerdict: "new",
			Tags:           []string{"ml", "transformers"},
			Connections: []models.Connection{
				{ReadingID: "bf_r0", Reason: "Related overview of attention mechanisms"},
			},
			Timestamp: time.Now().UTC(),
		})

		if embed.Title != "Reading completed" {
			t.Fatalf("Title = %q, want %q", embed.Title, "Reading completed")
		}
		wantDesc := "New reading saved: https://example.com/article"
		if embed.Description != wantDesc {
			t.Fatalf("Description = %q, want %q", embed.Description, wantDesc)
		}
		if embed.Color != 0x57F287 {
			t.Fatalf("Color = %#x, want 0x57F287 (green)", embed.Color)
		}
		if embed.URL != "" {
			t.Fatalf("URL = %q, want empty (no PR for readings)", embed.URL)
		}
		if !containsField(embed.Fields, "TL;DR", "Article explains how transformers work") {
			t.Fatalf("fields = %#v, want TL;DR field", embed.Fields)
		}
		if !containsField(embed.Fields, "Tags", "ml, transformers") {
			t.Fatalf("fields = %#v, want Tags field", embed.Fields)
		}
		if !containsField(embed.Fields, "Connections", "attention mechanisms") {
			t.Fatalf("fields = %#v, want Connections field", embed.Fields)
		}
		// Should NOT have PR URL field
		if containsField(embed.Fields, "Pull Request", "") {
			t.Fatal("reading embed should not have Pull Request field")
		}
	})

	t.Run("reading completed nothing new verdict", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:           EventTaskCompleted,
			TaskID:         "bf_r2",
			TaskMode:       "read",
			TLDR:           "Summarizes transformer architecture basics",
			NoveltyVerdict: "nothing new",
			Tags:           []string{"ml"},
			Connections: []models.Connection{
				{ReadingID: "bf_r0", Reason: "Covers same attention mechanism overview"},
			},
			Timestamp: time.Now().UTC(),
		})

		if embed.Title != "Reading completed" {
			t.Fatalf("Title = %q, want %q", embed.Title, "Reading completed")
		}
		if embed.Color != 0x95A5A6 {
			t.Fatalf("Color = %#x, want 0x95A5A6 (grey)", embed.Color)
		}
		if !strings.Contains(embed.Description, "Nothing new") {
			t.Fatalf("Description = %q, want 'Nothing new' language", embed.Description)
		}
		if !containsField(embed.Fields, "TL;DR", "transformer architecture") {
			t.Fatalf("fields = %#v, want TL;DR field", embed.Fields)
		}
		if !containsField(embed.Fields, "Most Similar", "attention mechanism") {
			t.Fatalf("fields = %#v, want Most Similar field with connection reason", embed.Fields)
		}
	})

	t.Run("reading completed duplicate verdict", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:           EventTaskCompleted,
			TaskID:         "bf_r3",
			TaskMode:       "read",
			TLDR:           "Same article about transformers",
			NoveltyVerdict: "duplicate",
			Timestamp:      time.Now().UTC(),
		})

		if embed.Title != "Reading completed" {
			t.Fatalf("Title = %q, want %q", embed.Title, "Reading completed")
		}
		if embed.Color != 0x95A5A6 {
			t.Fatalf("Color = %#x, want 0x95A5A6 (grey)", embed.Color)
		}
		if !strings.Contains(embed.Description, "Already read") {
			t.Fatalf("Description = %q, want 'Already read' language", embed.Description)
		}
		if !containsField(embed.Fields, "TL;DR", "transformers") {
			t.Fatalf("fields = %#v, want TL;DR field", embed.Fields)
		}
		// Should NOT have tags or connections fields for duplicate
		if containsField(embed.Fields, "Tags", "") {
			t.Fatal("duplicate embed should not have Tags field")
		}
		if containsField(embed.Fields, "Connections", "") {
			t.Fatal("duplicate embed should not have Connections field")
		}
	})

	t.Run("reading completed empty verdict", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:      EventTaskCompleted,
			TaskID:    "bf_r5",
			TaskMode:  "read",
			TLDR:      "Overview of Go concurrency patterns",
			Timestamp: time.Now().UTC(),
		})

		if embed.Title != "Reading completed" {
			t.Fatalf("Title = %q, want %q", embed.Title, "Reading completed")
		}
		if embed.Color != 0x57F287 {
			t.Fatalf("Color = %#x, want 0x57F287 (green)", embed.Color)
		}
		if containsField(embed.Fields, "Verdict", "") {
			t.Fatal("embed should not have Verdict field when NoveltyVerdict is empty")
		}
	})

	t.Run("reading failed shows reading-specific title", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:         EventTaskFailed,
			TaskID:       "bf_r4",
			TaskMode:     "read",
			Message:      "embedding API returned 429",
			AgentLogTail: "rate limited",
			Timestamp:    time.Now().UTC(),
		})

		if embed.Title != "Reading failed" {
			t.Fatalf("Title = %q, want %q", embed.Title, "Reading failed")
		}
		if embed.Color != 0xED4245 {
			t.Fatalf("Color = %#x, want 0xED4245 (red)", embed.Color)
		}
		if !strings.Contains(embed.Description, "Reading") {
			t.Fatalf("Description = %q, want reading-specific failure message", embed.Description)
		}
		if !containsField(embed.Fields, "Failure", "429") {
			t.Fatalf("fields = %#v, want Failure field", embed.Fields)
		}
		if !containsField(embed.Fields, "Log Tail", "rate limited") {
			t.Fatalf("fields = %#v, want Log Tail field", embed.Fields)
		}
	})

	t.Run("running stays concise", func(t *testing.T) {
		embed := discordEmbedForEvent(Event{
			Type:      EventTaskRunning,
			TaskID:    "bf_1",
			Timestamp: time.Now().UTC(),
		})
		if len(embed.Fields) != 1 {
			t.Fatalf("embed fields = %#v, want 1 concise field", embed.Fields)
		}
		if len(embed.Description) > 100 {
			t.Fatalf("description too long: %d", len(embed.Description))
		}
	})
}

func assertDiscordPayload(t *testing.T, body []byte, wantTitle, wantDescription string) {
	t.Helper()

	var payload discord.MessagePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("embeds length = %d, want 1", len(payload.Embeds))
	}
	embed := payload.Embeds[0]
	if embed.Title != wantTitle {
		t.Fatalf("embed title = %q, want %q", embed.Title, wantTitle)
	}
	if !strings.Contains(embed.Description, wantDescription) {
		t.Fatalf("embed description = %q, want to contain %q", embed.Description, wantDescription)
	}
	if payload.AllowedMentions == nil {
		t.Fatal("allowed_mentions missing")
	}
	if len(payload.AllowedMentions.Parse) != 0 {
		t.Fatalf("allowed_mentions.parse = %#v, want empty", payload.AllowedMentions.Parse)
	}
}

func containsField(fields []discord.EmbedField, name, contains string) bool {
	for _, field := range fields {
		if field.Name == name && (contains == "" || strings.Contains(field.Value, contains)) {
			return true
		}
	}
	return false
}

func TestButtonsForEvent(t *testing.T) {
	tests := []struct {
		name      string
		event     Event
		wantLabel string // empty means no buttons
	}{
		{"running gets cancel", Event{Type: EventTaskRunning, TaskID: "bf_1"}, "Cancel"},
		{"created gets cancel", Event{Type: EventTaskCreated, TaskID: "bf_1"}, "Cancel"},
		{"recovering gets cancel", Event{Type: EventTaskRecovering, TaskID: "bf_1"}, "Cancel"},
		{"failed ready gets retry", Event{Type: EventTaskFailed, TaskID: "bf_1", ReadyForRetry: true}, "Retry"},
		{"failed not ready no buttons", Event{Type: EventTaskFailed, TaskID: "bf_1"}, ""},
		{"failed retry limit no buttons", Event{Type: EventTaskFailed, TaskID: "bf_1", ReadyForRetry: true, RetryLimitReached: true}, ""},
		{"interrupted ready gets retry", Event{Type: EventTaskInterrupted, TaskID: "bf_1", ReadyForRetry: true}, "Retry"},
		{"interrupted not ready no buttons", Event{Type: EventTaskInterrupted, TaskID: "bf_1"}, ""},
		{"cancelled no buttons", Event{Type: EventTaskCancelled, TaskID: "bf_1"}, ""},
		{"cancelled ready gets retry", Event{Type: EventTaskCancelled, TaskID: "bf_1", ReadyForRetry: true}, "Retry"},
		{"cancelled retry limit no buttons", Event{Type: EventTaskCancelled, TaskID: "bf_1", ReadyForRetry: true, RetryLimitReached: true}, ""},
		{"completed no buttons", Event{Type: EventTaskCompleted, TaskID: "bf_1"}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			btns := buttonsForEvent(tc.event)
			if tc.wantLabel == "" {
				if len(btns) != 0 {
					t.Errorf("got %d buttons, want 0", len(btns))
				}
				return
			}
			if len(btns) != 1 {
				t.Fatalf("got %d buttons, want 1", len(btns))
			}
			if btns[0].Label != tc.wantLabel {
				t.Errorf("label = %q, want %q", btns[0].Label, tc.wantLabel)
			}
		})
	}
}
