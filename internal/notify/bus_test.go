package notify

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/rs/zerolog/log"
)

// --- test helpers ---

type collectingNotifier struct {
	mu     sync.Mutex
	events []Event
}

func (n *collectingNotifier) Notify(e Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, e)
	return nil
}

func (n *collectingNotifier) getEvents() []Event {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Event, len(n.events))
	copy(out, n.events)
	return out
}

type errorNotifier struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (n *errorNotifier) Notify(e Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, e)
	return n.err
}

func (n *errorNotifier) getEvents() []Event {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Event, len(n.events))
	copy(out, n.events)
	return out
}

type namedErrorNotifier struct {
	*errorNotifier
	name string
}

func (n *namedErrorNotifier) Name() string { return n.name }

type blockingNotifier struct {
	started chan struct{}
	unblock chan struct{}
}

func (n *blockingNotifier) Notify(Event) error {
	n.started <- struct{}{}
	<-n.unblock
	return nil
}

func TestNewEvent_PopulatesCoreFieldsFromTask(t *testing.T) {
	task := &models.Task{
		ID:           "bf_abc123",
		RepoURL:      "https://github.com/org/repo",
		Prompt:       "fix the bug",
		ReplyChannel: "sms:+15551234567",
	}

	before := time.Now().UTC()
	event := NewEvent(EventTaskCompleted, task)
	after := time.Now().UTC()

	if event.Type != EventTaskCompleted {
		t.Errorf("Type = %q, want %q", event.Type, EventTaskCompleted)
	}
	if event.TaskID != "bf_abc123" {
		t.Errorf("TaskID = %q, want %q", event.TaskID, "bf_abc123")
	}
	if event.RepoURL != "https://github.com/org/repo" {
		t.Errorf("RepoURL = %q", event.RepoURL)
	}
	if event.Prompt != "fix the bug" {
		t.Errorf("Prompt = %q", event.Prompt)
	}
	if event.ReplyChannel != "sms:+15551234567" {
		t.Errorf("ReplyChannel = %q, want %q", event.ReplyChannel, "sms:+15551234567")
	}
	if event.Timestamp.Before(before) || event.Timestamp.After(after) {
		t.Errorf("Timestamp = %v, want between %v and %v", event.Timestamp, before, after)
	}
}

func TestNewEvent_AllEventTypes(t *testing.T) {
	task := &models.Task{ID: "bf_1", RepoURL: "https://github.com/org/repo", Prompt: "do it"}

	types := []EventType{
		EventTaskCreated,
		EventTaskRunning,
		EventTaskCompleted,
		EventTaskFailed,
		EventTaskNeedsInput,
		EventTaskInterrupted,
		EventTaskRecovering,
		EventTaskCancelled,
		EventTaskRetry,
	}

	for _, et := range types {
		t.Run(string(et), func(t *testing.T) {
			event := NewEvent(et, task)
			if event.Type != et {
				t.Errorf("Type = %q, want %q", event.Type, et)
			}
			if event.TaskID != task.ID {
				t.Errorf("TaskID = %q", event.TaskID)
			}
		})
	}
}

func TestNewEvent_WithContainerStatus(t *testing.T) {
	task := &models.Task{ID: "bf_1", RepoURL: "https://github.com/org/repo", Prompt: "do it"}

	event := NewEvent(EventTaskCompleted, task, WithContainerStatus("https://github.com/org/repo/pull/42", "success", "last 5 lines"))

	if event.PRURL != "https://github.com/org/repo/pull/42" {
		t.Errorf("PRURL = %q", event.PRURL)
	}
	if event.Message != "success" {
		t.Errorf("Message = %q", event.Message)
	}
	if event.AgentLogTail != "last 5 lines" {
		t.Errorf("AgentLogTail = %q", event.AgentLogTail)
	}
}

// --- EventBus tests ---

func TestEventBus_FanOutDelivery(t *testing.T) {
	bus := NewEventBus()

	sub1 := &collectingNotifier{}
	sub2 := &collectingNotifier{}
	bus.Subscribe(sub1)
	bus.Subscribe(sub2)

	event := Event{Type: EventTaskCompleted, TaskID: "bf_1", Timestamp: time.Now()}
	bus.Emit(event)

	bus.Close()

	events1 := sub1.getEvents()
	events2 := sub2.getEvents()
	if len(events1) != 1 {
		t.Fatalf("sub1 got %d events, want 1", len(events1))
	}
	if len(events2) != 1 {
		t.Fatalf("sub2 got %d events, want 1", len(events2))
	}
	if events1[0].TaskID != "bf_1" {
		t.Errorf("sub1 TaskID = %q", events1[0].TaskID)
	}
	if events2[0].TaskID != "bf_1" {
		t.Errorf("sub2 TaskID = %q", events2[0].TaskID)
	}
}

func TestEventBus_SubscriberIsolation(t *testing.T) {
	bus := NewEventBus()

	failing := &errorNotifier{err: errors.New("boom")}
	healthy := &collectingNotifier{}
	bus.Subscribe(failing)
	bus.Subscribe(healthy)

	bus.Emit(Event{Type: EventTaskFailed, TaskID: "bf_2", Timestamp: time.Now()})
	bus.Close()

	// The healthy subscriber must still receive the event despite the first one failing.
	got := healthy.getEvents()
	if len(got) != 1 {
		t.Fatalf("healthy subscriber got %d events, want 1", len(got))
	}
	// The failing subscriber should also have received the event (error is logged, not fatal).
	failGot := failing.getEvents()
	if len(failGot) != 1 {
		t.Fatalf("failing subscriber got %d events, want 1", len(failGot))
	}
}

func TestEventBus_LogsSubscriberIdentityOnFailure(t *testing.T) {
	var buf bytes.Buffer
	orig := log.Logger
	log.Logger = log.Logger.Output(&buf)
	t.Cleanup(func() {
		log.Logger = orig
	})

	bus := NewEventBus()
	failing := &namedErrorNotifier{
		errorNotifier: &errorNotifier{err: errors.New("boom")},
		name:          "webhook",
	}
	bus.Subscribe(failing)

	bus.Emit(Event{Type: EventTaskFailed, TaskID: "bf_99", Timestamp: time.Now()})
	bus.Close()

	out := buf.String()
	if !strings.Contains(out, `"event":"task.failed"`) {
		t.Fatalf("log output missing event type: %s", out)
	}
	if !strings.Contains(out, `"task_id":"bf_99"`) {
		t.Fatalf("log output missing task id: %s", out)
	}
	if !strings.Contains(out, `"channel":"webhook"`) {
		t.Fatalf("log output missing subscriber channel: %s", out)
	}
}

func TestEventBus_AsyncDelivery(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	blocker := &blockingNotifier{
		started: make(chan struct{}, 1),
		unblock: make(chan struct{}),
	}
	bus.Subscribe(blocker)

	// Emit should return immediately even though the subscriber blocks.
	done := make(chan struct{})
	go func() {
		bus.Emit(Event{Type: EventTaskRunning, TaskID: "bf_3", Timestamp: time.Now()})
		close(done)
	}()

	select {
	case <-done:
		// Emit returned without blocking — good.
	case <-time.After(time.Second):
		t.Fatal("Emit blocked on subscriber execution")
	}

	// Clean up: unblock the subscriber so Close() can drain.
	<-blocker.started
	close(blocker.unblock)
}

func TestEventBus_GracefulShutdown(t *testing.T) {
	bus := NewEventBus()

	sub := &collectingNotifier{}
	bus.Subscribe(sub)

	// Emit multiple events.
	for i := range 10 {
		bus.Emit(Event{Type: EventTaskCompleted, TaskID: "bf_" + time.Now().Format("150405") + string(rune('a'+i)), Timestamp: time.Now()})
	}

	// Close waits for all pending events to drain.
	bus.Close()

	got := sub.getEvents()
	if len(got) != 10 {
		t.Fatalf("after Close(), subscriber got %d events, want 10", len(got))
	}
}

func TestEventBus_EmitAfterClose(t *testing.T) {
	bus := NewEventBus()

	sub := &collectingNotifier{}
	bus.Subscribe(sub)
	bus.Close()

	// Emit after Close must not panic — event is silently dropped.
	bus.Emit(Event{Type: EventTaskFailed, TaskID: "bf_late", Timestamp: time.Now()})

	got := sub.getEvents()
	if len(got) != 0 {
		t.Fatalf("subscriber got %d events after Close, want 0", len(got))
	}
}

func TestEventBus_ConcurrentEmitAndClose(t *testing.T) {
	for range 100 {
		bus := NewEventBus()
		sub := &collectingNotifier{}
		bus.Subscribe(sub)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			bus.Emit(Event{Type: EventTaskRunning, TaskID: "bf_race", Timestamp: time.Now()})
		}()
		go func() {
			defer wg.Done()
			bus.Close()
		}()

		wg.Wait()
	}
}

func TestEventBus_NoSubscribers(t *testing.T) {
	bus := NewEventBus()

	// Emit with no subscribers should not panic or block.
	done := make(chan struct{})
	go func() {
		bus.Emit(Event{Type: EventTaskRunning, TaskID: "bf_orphan", Timestamp: time.Now()})
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("Emit blocked with no subscribers")
	}

	bus.Close()
}

func TestEventBus_CloseWithTimeout(t *testing.T) {
	bus := NewEventBus()

	blocker := &blockingNotifier{
		started: make(chan struct{}, 1),
		unblock: make(chan struct{}),
	}
	bus.Subscribe(blocker)

	bus.Emit(Event{Type: EventTaskRunning, TaskID: "bf_timeout", Timestamp: time.Now()})
	<-blocker.started

	errCh := make(chan error, 1)
	go func() {
		errCh <- bus.CloseWithTimeout(50 * time.Millisecond)
	}()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(time.Second):
		t.Fatal("CloseWithTimeout blocked past its timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseWithTimeout error = %v, want DeadlineExceeded", err)
	}

	close(blocker.unblock)

	doneCh := make(chan error, 1)
	go func() {
		doneCh <- bus.CloseWithTimeout(time.Second)
	}()

	select {
	case err = <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("CloseWithTimeout did not finish after subscriber unblocked")
	}
	if err != nil {
		t.Fatalf("CloseWithTimeout after unblock = %v, want nil", err)
	}
}
