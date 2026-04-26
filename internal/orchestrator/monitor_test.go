package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/orchestrator/outputs"
	"github.com/brian-bell/backlite/internal/store"
)

func TestMonitorCancelled_DecrementsRunning(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_cancel_run",
		Status:      models.TaskStatusCancelled,
		ContainerID: "abc123",
		StartedAt:   &now,
		CompletedAt: &now,
	})

	bus, notifier := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	o.monitorCancelled(context.Background())
	bus.Close()

	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	task, _ := s.GetTask(context.Background(), "bf_cancel_run")
	if task.ContainerID != "" {
		t.Errorf("containerID = %q, want empty (should be cleared after cleanup)", task.ContainerID)
	}
	// Verify a cancelled event with ReadyForRetry was emitted after cleanup
	events := notifier.eventTypes()
	if len(events) != 1 || events[0] != notify.EventTaskCancelled {
		t.Errorf("events = %v, want [task.cancelled]", events)
	}
	notifier.mu.Lock()
	if !notifier.events[0].ReadyForRetry {
		t.Error("expected ReadyForRetry=true on post-cleanup cancelled event")
	}
	notifier.mu.Unlock()
}

func TestMonitorCancelled_NoContainer_SetsReadyForRetry(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_cancel_prov",
		Status:      models.TaskStatusCancelled,
		CompletedAt: &now,
	})

	bus, notifier := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 0

	o.monitorCancelled(context.Background())
	bus.Close()

	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	task, _ := s.GetTask(context.Background(), "bf_cancel_prov")
	if !task.ReadyForRetry {
		t.Error("expected ReadyForRetry=true for cancelled task without container")
	}

	events := notifier.eventTypes()
	if len(events) != 1 || events[0] != notify.EventTaskCancelled {
		t.Errorf("events = %v, want [task.cancelled]", events)
	}
	notifier.mu.Lock()
	if !notifier.events[0].ReadyForRetry {
		t.Error("expected ReadyForRetry=true on event")
	}
	notifier.mu.Unlock()
}

func TestMonitorCancelled_AtCapStillSetsReadyForRetry(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:             "bf_cancel_cap",
		Status:         models.TaskStatusCancelled,
		UserRetryCount: 2, // at cap (MaxUserRetries=2)
		CompletedAt:    &now,
	})

	bus, notifier := newTestBus()
	o := newTestOrchestrator(s, bus)

	// Run monitor twice — should NOT emit twice
	o.monitorCancelled(context.Background())
	o.monitorCancelled(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_cancel_cap")
	if !task.ReadyForRetry {
		t.Error("expected ReadyForRetry=true even at cap (signals cleanup done)")
	}

	events := notifier.eventTypes()
	if len(events) != 1 {
		t.Errorf("expected exactly 1 event (not re-emitted on second tick), got %d", len(events))
	}
	notifier.mu.Lock()
	if !notifier.events[0].RetryLimitReached {
		t.Error("expected RetryLimitReached=true on event")
	}
	if notifier.events[0].ReadyForRetry {
		t.Error("expected ReadyForRetry=false on event when at cap")
	}
	notifier.mu.Unlock()
}

func TestMonitorCancelled_RecoveringTaskCancelled(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_cancel_recov",
		Status:      models.TaskStatusCancelled,
		ContainerID: "def456",
		StartedAt:   &now,
		CompletedAt: &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	o.monitorCancelled(context.Background())

	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	task, _ := s.GetTask(context.Background(), "bf_cancel_recov")
	if task.ContainerID != "" {
		t.Errorf("containerID = %q, want empty", task.ContainerID)
	}
}

func TestHandleCompletion_Success(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_ok",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_ok")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		ExitCode: 0,
		PRURL:    "https://github.com/test/repo/pull/1",
	})
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_ok")
	if task.Status != models.TaskStatusCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.PRURL != "https://github.com/test/repo/pull/1" {
		t.Errorf("PRURL = %q, want PR URL", task.PRURL)
	}
	if task.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCompleted {
		t.Errorf("expected [task.completed], got %v", types)
	}
}

// TestHandleCompletion_SelfReview_ChainsReviewTask covers the end-to-end
// self-review chain via handleCompletion: a successful code task with
// SelfReview=true and a non-empty PR URL produces a child review task with
// the parent's PR URL synthesized into its prompt.
func TestHandleCompletion_SelfReview_ChainsReviewTask(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_selfrev",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeCode,
		Harness:     models.HarnessClaudeCode,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "Refactor auth handler",
		SelfReview:  true,
		ContainerID: "cont-self",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_selfrev")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		ExitCode: 0,
		PRURL:    "https://github.com/test/repo/pull/77",
	})
	bus.Close()

	// Parent persisted as completed.
	parent, _ := s.GetTask(context.Background(), "bf_selfrev")
	if parent.Status != models.TaskStatusCompleted {
		t.Errorf("parent.Status = %q, want completed", parent.Status)
	}

	// One child task should exist with the right shape.
	tasks, _ := s.ListTasks(context.Background(), store.TaskFilter{})
	var child *models.Task
	for _, t2 := range tasks {
		if t2.ID == parent.ID {
			continue
		}
		child = t2
	}
	if child == nil {
		t.Fatalf("no child task created; tasks = %+v", tasks)
	}
	if child.TaskMode != models.TaskModeReview {
		t.Errorf("child.TaskMode = %q, want review", child.TaskMode)
	}
	if child.ParentTaskID == nil || *child.ParentTaskID != parent.ID {
		t.Errorf("child.ParentTaskID = %v, want %q", child.ParentTaskID, parent.ID)
	}
	if child.MaxBudgetUSD != 2.0 {
		t.Errorf("child.MaxBudgetUSD = %v, want 2.0", child.MaxBudgetUSD)
	}
	if child.Harness != models.HarnessClaudeCode {
		t.Errorf("child.Harness = %q, want claude_code (inherited)", child.Harness)
	}

	// Two events: parent.completed, child.created. Order matters.
	types := n.eventTypes()
	if len(types) != 2 {
		t.Fatalf("event types = %v, want [task.completed, task.created]", types)
	}
	if types[0] != notify.EventTaskCompleted {
		t.Errorf("event 0 = %q, want task.completed", types[0])
	}
	if types[1] != notify.EventTaskCreated {
		t.Errorf("event 1 = %q, want task.created", types[1])
	}
}

// TestHandleCompletion_SelfReview_NoPR_NoChain pins that a successful code
// task without a PR URL doesn't trigger a chain (PR URL is the contract).
func TestHandleCompletion_SelfReview_NoPR_NoChain(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_nopr",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeCode,
		Harness:     models.HarnessClaudeCode,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "Refactor",
		SelfReview:  true,
		ContainerID: "cont-nopr",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_nopr")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		ExitCode: 0,
		PRURL:    "",
	})
	bus.Close()

	tasks, _ := s.ListTasks(context.Background(), store.TaskFilter{})
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1 (no chain when no PR URL)", len(tasks))
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCompleted {
		t.Errorf("event types = %v, want [task.completed] only", types)
	}
}

func TestHandleCompletion_CompleteFlagOverridesExitCode(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_complete_flag",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		ContainerID: "cont1",
		StartedAt:   &now,
		Error:       "previous error",
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_complete_flag")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		ExitCode: 1,
		PRURL:    "https://github.com/test/repo/pull/2",
	})
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_complete_flag")
	if task.Status != models.TaskStatusCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.Error != "" {
		t.Errorf("error = %q, want empty", task.Error)
	}
	if task.PRURL != "https://github.com/test/repo/pull/2" {
		t.Errorf("PRURL = %q, want PR URL", task.PRURL)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCompleted {
		t.Errorf("expected [task.completed], got %v", types)
	}
}

// TestHandleCompletion_AgentReportedFailureExit0 pins that an agent reporting
// complete=false with an error must mark the task failed even when the
// container exits 0. Without this, skill-authored failure branches (no repo
// URL, mode stubs, etc.) would silently be recorded as success whenever the
// underlying harness happened to exit 0.
func TestHandleCompletion_AgentReportedFailureExit0(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_skill_fail",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_skill_fail")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: false,
		ExitCode: 0,
		Error:    "no repo URL in prompt",
	})
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_skill_fail")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if task.Error != "no repo URL in prompt" {
		t.Errorf("error = %q, want 'no repo URL in prompt'", task.Error)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", types)
	}
}

func TestHandleCompletion_NeedsInput(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_input",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_input")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:       true,
		ExitCode:   1,
		NeedsInput: true,
		Question:   "What is the database password?",
	})
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_input")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if task.Error != "agent needs input" {
		t.Errorf("error = %q, want 'agent needs input'", task.Error)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskNeedsInput {
		t.Errorf("expected [task.needs_input], got %v", types)
	}
}

func TestHandleCompletion_Failure(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_fail",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_fail")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		ExitCode: 1,
		Error:    "something went wrong",
	})
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_fail")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if task.Error != "something went wrong" {
		t.Errorf("error = %q, want 'something went wrong'", task.Error)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", types)
	}

	// After failure, task should be marked ready for retry (under cap)
	if !task.ReadyForRetry {
		t.Error("expected ReadyForRetry=true after failure (under retry cap)")
	}
	n.mu.Lock()
	if !n.events[0].ReadyForRetry {
		t.Error("expected ReadyForRetry=true on failed event")
	}
	n.mu.Unlock()
}

func TestHandleCompletion_PropagatesInferredFields(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_inferred",
		Status:      models.TaskStatusRunning,
		TaskMode:    "auto",
		Prompt:      "fix the bug in https://github.com/test/repo",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_inferred")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:         true,
		Complete:     true,
		PRURL:        "https://github.com/test/repo/pull/99",
		RepoURL:      "https://github.com/test/repo",
		TargetBranch: "main",
		TaskMode:     "code",
	})
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_inferred")
	if task.Status != models.TaskStatusCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.RepoURL != "https://github.com/test/repo" {
		t.Errorf("RepoURL = %q, want %q", task.RepoURL, "https://github.com/test/repo")
	}
	if task.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want %q", task.TargetBranch, "main")
	}
	if task.TaskMode != "code" {
		t.Errorf("TaskMode = %q, want %q", task.TaskMode, "code")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(n.events))
	}
	if n.events[0].RepoURL != "https://github.com/test/repo" {
		t.Errorf("event RepoURL = %q, want %q", n.events[0].RepoURL, "https://github.com/test/repo")
	}
	if n.events[0].TaskMode != "code" {
		t.Errorf("event TaskMode = %q, want %q", n.events[0].TaskMode, "code")
	}
}

func TestHandleCompletion_ReadSuccess_EmbedsAndCreatesReading(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_ok",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "https://example.com/post",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1, 0.2, 0.3}}
	o := newTestOrchestrator(s, bus, withEmbedder(emb))
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_ok")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:            true,
		Complete:        true,
		TaskMode:        models.TaskModeRead,
		URL:             "https://example.com/post",
		Title:           "Post title",
		TLDR:            "short summary",
		Tags:            []string{"ai", "systems"},
		Keywords:        []string{"retrieval"},
		People:          []string{"Ada"},
		Orgs:            []string{"Example Corp"},
		NoveltyVerdict:  "new",
		Connections:     []models.Connection{{ReadingID: "bf_other", Reason: "same topic"}},
		SummaryMarkdown: "# Long summary",
	})
	bus.Close()

	// Task should be completed.
	got, _ := s.GetTask(context.Background(), "bf_read_ok")
	if got.Status != models.TaskStatusCompleted {
		t.Errorf("task.Status = %q, want completed", got.Status)
	}

	// Embedder was called with the TL;DR.
	emb.mu.Lock()
	if len(emb.calls) != 1 || emb.calls[0] != "short summary" {
		t.Errorf("embedder calls = %v, want [short summary]", emb.calls)
	}
	emb.mu.Unlock()

	// UpsertReading received the reading (always upsert, regardless of force).
	s.mu.Lock()
	if len(s.upsertedReadings) != 1 {
		t.Fatalf("upsertedReadings = %d, want 1", len(s.upsertedReadings))
	}
	r := s.upsertedReadings[0]
	s.mu.Unlock()

	if r.URL != "https://example.com/post" {
		t.Errorf("reading.URL = %q", r.URL)
	}
	if r.Title != "Post title" {
		t.Errorf("reading.Title = %q", r.Title)
	}
	if r.TLDR != "short summary" {
		t.Errorf("reading.TLDR = %q", r.TLDR)
	}
	if r.NoveltyVerdict != "new" {
		t.Errorf("reading.NoveltyVerdict = %q", r.NoveltyVerdict)
	}
	if r.TaskID != "bf_read_ok" {
		t.Errorf("reading.TaskID = %q", r.TaskID)
	}
	if len(r.Embedding) != 3 || r.Embedding[0] != 0.1 {
		t.Errorf("reading.Embedding = %v, want [0.1 0.2 0.3]", r.Embedding)
	}
	if len(r.Tags) != 2 {
		t.Errorf("reading.Tags = %v", r.Tags)
	}
	if len(r.Connections) != 1 || r.Connections[0].ReadingID != "bf_other" {
		t.Errorf("reading.Connections = %+v", r.Connections)
	}
	if len(r.RawOutput) == 0 {
		t.Errorf("reading.RawOutput should be populated")
	}
	if !strings.HasPrefix(r.ID, "bf_") {
		t.Errorf("reading.ID = %q, want bf_-prefixed ULID", r.ID)
	}
	if r.CreatedAt.IsZero() {
		t.Errorf("reading.CreatedAt is zero, want non-zero")
	}

	// Emitted event should be task.completed with reading fields set.
	events := n.eventTypes()
	if len(events) != 1 || events[0] != notify.EventTaskCompleted {
		t.Fatalf("events = %v, want [task.completed]", events)
	}
	n.mu.Lock()
	e := n.events[0]
	n.mu.Unlock()
	if e.TLDR != "short summary" {
		t.Errorf("event.TLDR = %q", e.TLDR)
	}
	if e.NoveltyVerdict != "new" {
		t.Errorf("event.NoveltyVerdict = %q", e.NoveltyVerdict)
	}
	if len(e.Tags) != 2 {
		t.Errorf("event.Tags = %v", e.Tags)
	}
	if len(e.Connections) != 1 {
		t.Errorf("event.Connections = %+v", e.Connections)
	}
}

func TestHandleCompletion_ReadSuccess_PersistsCapturedContent(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_capture",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "https://example.com/cap",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1}}

	raw := []byte("<html><title>cap</title></html>")
	extracted := []byte("# cap\n\nbody")
	sidecar := []byte(`{
  "url":"https://example.com/cap",
  "content_type":"text/html; charset=utf-8",
  "content_status":"captured",
  "content_bytes":31,
  "extracted_bytes":11,
  "content_sha256":"abc123",
  "fetched_at":"2026-04-25T12:00:00Z"
}`)

	mockDocker := &mockDockerManager{
		readingRaw:       raw,
		readingExtracted: extracted,
		readingSidecar:   sidecar,
	}
	mockOut := &mockWriter{}
	o := newTestOrchestrator(s, bus,
		withEmbedder(emb),
		withDocker(mockDocker),
		withOutputs(mockOut),
	)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_capture")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:           true,
		Complete:       true,
		TaskMode:       models.TaskModeRead,
		URL:            "https://example.com/cap",
		TLDR:           "cap summary",
		NoveltyVerdict: "new",
	})
	bus.Close()

	// Reading row should carry the parsed sidecar fields.
	s.mu.Lock()
	if len(s.upsertedReadings) != 1 {
		t.Fatalf("upsertedReadings = %d, want 1", len(s.upsertedReadings))
	}
	r := s.upsertedReadings[0]
	s.mu.Unlock()

	if r.ContentType != "text/html; charset=utf-8" {
		t.Errorf("reading.ContentType = %q", r.ContentType)
	}
	if r.ContentStatus != "captured" {
		t.Errorf("reading.ContentStatus = %q, want captured", r.ContentStatus)
	}
	if r.ContentBytes != 31 {
		t.Errorf("reading.ContentBytes = %d, want 31", r.ContentBytes)
	}
	if r.ExtractedBytes != 11 {
		t.Errorf("reading.ExtractedBytes = %d, want 11", r.ExtractedBytes)
	}
	if r.ContentSHA256 != "abc123" {
		t.Errorf("reading.ContentSHA256 = %q", r.ContentSHA256)
	}
	if r.FetchedAt == nil {
		t.Error("reading.FetchedAt = nil, want non-nil")
	}

	// Outputs writer should have been called with the captured bytes, keyed
	// by the new reading's ID.
	if len(mockOut.readingSaves) != 1 {
		t.Fatalf("SaveReadingContent calls = %d, want 1", len(mockOut.readingSaves))
	}
	got := mockOut.readingSaves[0]
	if got.readingID != r.ID {
		t.Errorf("SaveReadingContent readingID = %q, want %q", got.readingID, r.ID)
	}
	if string(got.raw) != string(raw) {
		t.Errorf("SaveReadingContent raw mismatch")
	}
	if string(got.extracted) != string(extracted) {
		t.Errorf("SaveReadingContent extracted mismatch")
	}

	// Webhook event should carry content_status.
	n.mu.Lock()
	if len(n.events) != 1 {
		t.Fatalf("events = %d, want 1", len(n.events))
	}
	e := n.events[0]
	n.mu.Unlock()
	if e.ContentStatus != "captured" {
		t.Errorf("event.ContentStatus = %q, want captured", e.ContentStatus)
	}
	if e.ContentType != "text/html; charset=utf-8" {
		t.Errorf("event.ContentType = %q", e.ContentType)
	}
}

func TestHandleCompletion_ReadSuccess_PersistsNonHTMLRawByContentType(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_pdf",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "https://example.com/doc.pdf",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1}}
	raw := []byte("%PDF-1.4\nminimal pdf body\n")
	sidecar := []byte(`{
  "url":"https://example.com/doc.pdf",
  "content_type":"application/pdf",
  "content_status":"captured",
  "content_bytes":26,
  "extracted_bytes":0,
  "content_sha256":"pdf-sha",
  "fetched_at":"2026-04-25T12:00:00Z"
}`)
	root := t.TempDir()
	o := newTestOrchestrator(s, bus,
		withEmbedder(emb),
		withDocker(&mockDockerManager{
			readingRaw:       raw,
			readingExtracted: nil,
			readingSidecar:   sidecar,
		}),
		withOutputs(outputs.New(root)),
	)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_pdf")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:           true,
		Complete:       true,
		TaskMode:       models.TaskModeRead,
		URL:            "https://example.com/doc.pdf",
		TLDR:           "pdf summary",
		NoveltyVerdict: "new",
	})
	bus.Close()

	s.mu.Lock()
	if len(s.upsertedReadings) != 1 {
		s.mu.Unlock()
		t.Fatalf("upsertedReadings = %d, want 1", len(s.upsertedReadings))
	}
	r := s.upsertedReadings[0]
	s.mu.Unlock()

	dir := filepath.Join(root, "readings", r.ID)
	gotRaw, err := os.ReadFile(filepath.Join(dir, "raw.pdf"))
	if err != nil {
		t.Fatalf("read raw.pdf: %v", err)
	}
	if string(gotRaw) != string(raw) {
		t.Errorf("raw.pdf = %q, want %q", gotRaw, raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "raw.html")); !os.IsNotExist(err) {
		t.Errorf("raw.html should not exist for PDF capture, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "extracted.md")); !os.IsNotExist(err) {
		t.Errorf("extracted.md should not exist for PDF capture, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "content.json")); err != nil {
		t.Errorf("content.json should exist: %v", err)
	}
}

func TestHandleCompletion_ReadSuccess_RecordsFetchFailedWithoutPersisting(t *testing.T) {
	// When the in-container fetcher records fetch_failed (e.g. HTTP 4xx, TLS
	// error), no raw bytes accompany the sidecar. The reading row should
	// reflect the failure status and SaveReadingContent must not run — there
	// are no bytes to persist.
	cases := []struct {
		name    string
		status  string
		sidecar string
	}{
		{
			name:   "fetch_failed",
			status: "fetch_failed",
			sidecar: `{
				"url":"https://example.com/dead",
				"content_type":"",
				"content_status":"fetch_failed",
				"content_bytes":0,
				"extracted_bytes":0,
				"content_sha256":"",
				"fetched_at":"2026-04-25T12:00:00Z"
			}`,
		},
		{
			name:   "over_size_cap",
			status: "over_size_cap",
			sidecar: `{
				"url":"https://example.com/huge",
				"content_type":"",
				"content_status":"over_size_cap",
				"content_bytes":0,
				"extracted_bytes":0,
				"content_sha256":"",
				"fetched_at":"2026-04-25T12:00:00Z"
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMockStore()
			now := time.Now().UTC()
			s.CreateTask(context.Background(), &models.Task{
				ID:          "bf_read_" + tc.status,
				Status:      models.TaskStatusRunning,
				TaskMode:    models.TaskModeRead,
				Prompt:      "https://example.com/" + tc.status,
				ContainerID: "cont1",
				StartedAt:   &now,
			})

			bus, n := newTestBus()
			emb := &mockEmbedder{vector: []float32{0.1}}
			mockOut := &mockWriter{}
			o := newTestOrchestrator(s, bus,
				withEmbedder(emb),
				withDocker(&mockDockerManager{
					readingRaw:       nil, // no raw bytes on capture failure
					readingExtracted: nil,
					readingSidecar:   []byte(tc.sidecar),
				}),
				withOutputs(mockOut),
			)
			o.running = 1

			task, _ := s.GetTask(context.Background(), "bf_read_"+tc.status)
			o.handleCompletion(context.Background(), task, ContainerStatus{
				Done:           true,
				Complete:       true,
				TaskMode:       models.TaskModeRead,
				URL:            "https://example.com/" + tc.status,
				TLDR:           "summary via webfetch fallback",
				NoveltyVerdict: "new",
			})
			bus.Close()

			// Task itself must succeed: capture failure is non-fatal.
			got, _ := s.GetTask(context.Background(), "bf_read_"+tc.status)
			if got.Status != models.TaskStatusCompleted {
				t.Errorf("task.Status = %q, want completed (capture failure must not fail the task)", got.Status)
			}

			s.mu.Lock()
			if len(s.upsertedReadings) != 1 {
				s.mu.Unlock()
				t.Fatalf("upsertedReadings = %d, want 1", len(s.upsertedReadings))
			}
			r := s.upsertedReadings[0]
			s.mu.Unlock()

			if r.ContentStatus != tc.status {
				t.Errorf("reading.ContentStatus = %q, want %q", r.ContentStatus, tc.status)
			}
			if r.ContentBytes != 0 {
				t.Errorf("reading.ContentBytes = %d, want 0", r.ContentBytes)
			}

			if len(mockOut.readingSaves) != 0 {
				t.Errorf("SaveReadingContent must not run on %s; got %d calls",
					tc.status, len(mockOut.readingSaves))
			}

			// Webhook event must surface the failure status.
			n.mu.Lock()
			if len(n.events) != 1 {
				n.mu.Unlock()
				t.Fatalf("events = %d, want 1", len(n.events))
			}
			e := n.events[0]
			n.mu.Unlock()
			if e.ContentStatus != tc.status {
				t.Errorf("event.ContentStatus = %q, want %q", e.ContentStatus, tc.status)
			}
		})
	}
}

func TestHandleCompletion_ReadSuccess_NoCaptureLeavesContentStatusEmpty(t *testing.T) {
	// Legacy / pre-capture container: GetReadingContent returns all-nil.
	// The reading row gets ContentStatus="" and SaveReadingContent is not
	// invoked.
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_legacy",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "https://example.com/legacy",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1}}
	mockOut := &mockWriter{}
	o := newTestOrchestrator(s, bus,
		withEmbedder(emb),
		withDocker(&mockDockerManager{}), // returns all-nil bytes
		withOutputs(mockOut),
	)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_legacy")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		TaskMode: models.TaskModeRead,
		URL:      "https://example.com/legacy",
		TLDR:     "x",
	})
	bus.Close()

	s.mu.Lock()
	r := s.upsertedReadings[0]
	s.mu.Unlock()

	if r.ContentStatus != "" {
		t.Errorf("reading.ContentStatus = %q, want empty", r.ContentStatus)
	}
	if len(mockOut.readingSaves) != 0 {
		t.Errorf("SaveReadingContent should not be called, got %d", len(mockOut.readingSaves))
	}
}

func TestHandleCompletion_ReadSuccess_NoOrphanFilesWhenSidecarParsesNil(t *testing.T) {
	// When parseCapturedSidecar returns nil but raw bytes are present, the
	// row's ContentStatus stays "" and the API endpoints would 404 — so
	// SaveReadingContent must not run. Otherwise the bytes become permanent
	// orphans on disk. Cover every path that produces a nil parse.
	cases := []struct {
		name    string
		sidecar []byte
	}{
		{name: "sidecar nil", sidecar: nil},
		{name: "sidecar malformed JSON", sidecar: []byte(`{not json`)},
		{name: "sidecar missing content_status", sidecar: []byte(`{"url":"https://example.com/orphan","content_type":"text/html"}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMockStore()
			now := time.Now().UTC()
			s.CreateTask(context.Background(), &models.Task{
				ID:          "bf_read_orphan",
				Status:      models.TaskStatusRunning,
				TaskMode:    models.TaskModeRead,
				Prompt:      "https://example.com/orphan",
				ContainerID: "cont1",
				StartedAt:   &now,
			})

			bus, _ := newTestBus()
			emb := &mockEmbedder{vector: []float32{0.1}}
			mockOut := &mockWriter{}
			o := newTestOrchestrator(s, bus,
				withEmbedder(emb),
				withDocker(&mockDockerManager{
					readingRaw:       []byte("<html>hi</html>"),
					readingExtracted: []byte("# hi"),
					readingSidecar:   tc.sidecar,
				}),
				withOutputs(mockOut),
			)
			o.running = 1

			task, _ := s.GetTask(context.Background(), "bf_read_orphan")
			o.handleCompletion(context.Background(), task, ContainerStatus{
				Done:     true,
				Complete: true,
				TaskMode: models.TaskModeRead,
				URL:      "https://example.com/orphan",
				TLDR:     "x",
			})
			bus.Close()

			s.mu.Lock()
			r := s.upsertedReadings[0]
			s.mu.Unlock()

			if r.ContentStatus != "" {
				t.Errorf("reading.ContentStatus = %q, want empty", r.ContentStatus)
			}
			if len(mockOut.readingSaves) != 0 {
				t.Errorf("SaveReadingContent must not run; got %d calls", len(mockOut.readingSaves))
			}
		})
	}
}

func TestHandleCompletion_ReadSuccess_PersistFailureDoesNotFailTask(t *testing.T) {
	// On-disk persist is best-effort: the row is already committed, and
	// failing the task here would block retries on the duplicate guard.
	// Verify the task still completes and the reading row is upserted.
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_persist_fail",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "https://example.com/persist-fail",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1}}
	mockOut := &mockWriter{
		readingSaveErr: errors.New("disk full"),
	}
	o := newTestOrchestrator(s, bus,
		withEmbedder(emb),
		withDocker(&mockDockerManager{
			readingRaw:       []byte("<html>x</html>"),
			readingExtracted: []byte("# x"),
			readingSidecar: []byte(`{
				"url":"https://example.com/persist-fail",
				"content_type":"text/html",
				"content_status":"captured",
				"content_bytes":15,
				"extracted_bytes":3,
				"content_sha256":"abc",
				"fetched_at":"2026-04-25T12:00:00Z"
			}`),
		}),
		withOutputs(mockOut),
	)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_persist_fail")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:           true,
		Complete:       true,
		TaskMode:       models.TaskModeRead,
		URL:            "https://example.com/persist-fail",
		TLDR:           "x",
		NoveltyVerdict: "new",
	})
	bus.Close()

	// Reading row was upserted despite the disk failure.
	s.mu.Lock()
	if len(s.upsertedReadings) != 1 {
		s.mu.Unlock()
		t.Fatalf("upsertedReadings = %d, want 1", len(s.upsertedReadings))
	}
	r := s.upsertedReadings[0]
	s.mu.Unlock()

	finalTask, _ := s.GetTask(context.Background(), "bf_read_persist_fail")

	if r.ContentStatus != "captured" {
		t.Errorf("reading.ContentStatus = %q, want captured", r.ContentStatus)
	}
	// Task must complete, not fail.
	if finalTask.Status != models.TaskStatusCompleted {
		t.Errorf("task.Status = %q, want completed", finalTask.Status)
	}
	// task.completed should still be emitted.
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.events) != 1 {
		t.Fatalf("events = %d, want 1", len(n.events))
	}
	if n.events[0].Type != notify.EventTaskCompleted {
		t.Errorf("event = %q, want task.completed", n.events[0].Type)
	}
}

func TestHandleCompletion_ReadSuccess_AssignsUniqueReadingIDs(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	for i, url := range []string{"https://example.com/a", "https://example.com/b"} {
		id := fmt.Sprintf("bf_read_%d", i)
		s.CreateTask(context.Background(), &models.Task{
			ID:          id,
			Status:      models.TaskStatusRunning,
			TaskMode:    models.TaskModeRead,
			Prompt:      url,
			ContainerID: "cont1",
			StartedAt:   &now,
		})

		bus, _ := newTestBus()
		emb := &mockEmbedder{vector: []float32{0.1}}
		o := newTestOrchestrator(s, bus, withEmbedder(emb))
		o.running = 1

		task, _ := s.GetTask(context.Background(), id)
		o.handleCompletion(context.Background(), task, ContainerStatus{
			Done:     true,
			Complete: true,
			TaskMode: models.TaskModeRead,
			URL:      url,
			TLDR:     "summary " + url,
		})
		bus.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.upsertedReadings) != 2 {
		t.Fatalf("upsertedReadings = %d, want 2", len(s.upsertedReadings))
	}
	if s.upsertedReadings[0].ID == s.upsertedReadings[1].ID {
		t.Errorf("reading IDs collided: %q == %q", s.upsertedReadings[0].ID, s.upsertedReadings[1].ID)
	}
	for i, r := range s.upsertedReadings {
		if !strings.HasPrefix(r.ID, "bf_") {
			t.Errorf("reading[%d].ID = %q, want bf_-prefixed", i, r.ID)
		}
	}
}

func TestHandleCompletion_ReadEmptyURL_MarksTaskFailed(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	// Both status.URL and task.Prompt are empty — no fallback possible.
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_empty_url",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1}}
	o := newTestOrchestrator(s, bus, withEmbedder(emb))
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_empty_url")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		TaskMode: models.TaskModeRead,
		URL:      "",
		TLDR:     "short summary",
	})
	bus.Close()

	got, _ := s.GetTask(context.Background(), "bf_read_empty_url")
	if got.Status != models.TaskStatusFailed {
		t.Errorf("task.Status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "url") {
		t.Errorf("task.Error = %q, want something about url", got.Error)
	}

	s.mu.Lock()
	if len(s.upsertedReadings) != 0 {
		t.Errorf("no reading should be written for empty URL: upserted=%d", len(s.upsertedReadings))
	}
	s.mu.Unlock()

	// Embedder should not be called when URL is missing.
	emb.mu.Lock()
	if len(emb.calls) != 0 {
		t.Errorf("embedder calls = %v, want none for empty URL", emb.calls)
	}
	emb.mu.Unlock()

	events := n.eventTypes()
	if len(events) != 1 || events[0] != notify.EventTaskFailed {
		t.Errorf("events = %v, want [task.failed]", events)
	}
}

// TestHandleCompletion_ReadDuplicate_SkipsWrite verifies that a non-forced
// duplicate reading completes successfully without overwriting the existing row,
// without copying pre-fetched files out of the container, and without writing
// any captured artifacts to disk. The existing row stays untouched.
func TestHandleCompletion_ReadDuplicate_SkipsWrite(t *testing.T) {
	const url = "https://example.com/post"
	const existingID = "bf_01HX_PRESERVE_EXISTING__"

	s := newMockStore()
	now := time.Now().UTC()
	s.readingsByURL[url] = &models.Reading{
		ID:            existingID,
		URL:           url,
		Title:         "old title",
		ContentStatus: "captured",
		ContentSHA256: "old-sha",
		CreatedAt:     now.Add(-72 * time.Hour),
	}
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_dup",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Force:       false,
		Prompt:      url,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1}}

	// Track GetReadingContent invocations: a duplicate must not even attempt
	// to copy pre-fetched files out of the container.
	var contentCalls int
	mockDocker := &mockDockerManager{
		readingContentFn: func(_ context.Context, _ string) (raw, extracted, sidecar []byte, err error) {
			contentCalls++
			return nil, nil, nil, nil
		},
	}
	mockOut := &mockWriter{}

	o := newTestOrchestrator(s, bus,
		withEmbedder(emb),
		withDocker(mockDocker),
		withOutputs(mockOut),
	)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_dup")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:           true,
		Complete:       true,
		TaskMode:       models.TaskModeRead,
		URL:            url,
		TLDR:           "already read",
		NoveltyVerdict: "duplicate",
	})
	bus.Close()

	got, _ := s.GetTask(context.Background(), "bf_read_dup")
	if got.Status != models.TaskStatusCompleted {
		t.Errorf("task.Status = %q, want completed", got.Status)
	}

	s.mu.Lock()
	if len(s.upsertedReadings) != 0 {
		t.Errorf("upsertedReadings = %d, want 0 for non-forced duplicate", len(s.upsertedReadings))
	}
	preserved := s.readingsByURL[url]
	s.mu.Unlock()

	if preserved == nil || preserved.ID != existingID || preserved.Title != "old title" || preserved.ContentSHA256 != "old-sha" {
		t.Errorf("existing row was modified; got %+v", preserved)
	}

	if contentCalls != 0 {
		t.Errorf("GetReadingContent was called %d time(s) for a non-forced duplicate; pre-fetched files must be discarded", contentCalls)
	}
	if len(mockOut.readingSaves) != 0 {
		t.Errorf("SaveReadingContent was called %d time(s) for a non-forced duplicate; want 0", len(mockOut.readingSaves))
	}

	// Embedder should not be called for duplicates (no embedding to write).
	emb.mu.Lock()
	if len(emb.calls) != 0 {
		t.Errorf("embedder calls = %v, want none for duplicate", emb.calls)
	}
	emb.mu.Unlock()

	events := n.eventTypes()
	if len(events) != 1 || events[0] != notify.EventTaskCompleted {
		t.Errorf("events = %v, want [task.completed]", events)
	}
}

// TestHandleCompletion_ReadForce_OverwritesSameID verifies that when a forced
// read task targets a URL that already has a reading row, the upsert keeps the
// existing reading id and the on-disk persister is invoked under that same id.
// Without this guarantee, force=true would orphan the prior on-disk artifacts
// (under the old id) while writing fresh ones under a never-stored id.
func TestHandleCompletion_ReadForce_OverwritesSameID(t *testing.T) {
	const existingID = "bf_01HX_OVERWRITE_TARGET___"
	const url = "https://example.com/refresh-me"

	s := newMockStore()
	now := time.Now().UTC()
	s.readingsByURL[url] = &models.Reading{
		ID:            existingID,
		URL:           url,
		Title:         "old title",
		ContentStatus: "captured",
		ContentSHA256: "old-sha",
		CreatedAt:     now.Add(-24 * time.Hour),
	}
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_force_overwrite",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Force:       true,
		Prompt:      url,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.9}}
	mockOut := &mockWriter{}
	o := newTestOrchestrator(s, bus,
		withEmbedder(emb),
		withDocker(&mockDockerManager{
			readingRaw:       []byte("<html>fresh</html>"),
			readingExtracted: []byte("# fresh"),
			readingSidecar: []byte(`{
				"url":"` + url + `",
				"content_type":"text/html",
				"content_status":"captured",
				"content_bytes":18,
				"extracted_bytes":7,
				"content_sha256":"new-sha",
				"fetched_at":"2026-04-25T12:00:00Z"
			}`),
		}),
		withOutputs(mockOut),
	)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_force_overwrite")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:           true,
		Complete:       true,
		TaskMode:       models.TaskModeRead,
		URL:            url,
		Title:          "fresh title",
		TLDR:           "fresh tldr",
		NoveltyVerdict: "new",
	})
	bus.Close()

	s.mu.Lock()
	if len(s.upsertedReadings) != 1 {
		s.mu.Unlock()
		t.Fatalf("upsertedReadings = %d, want 1", len(s.upsertedReadings))
	}
	upserted := s.upsertedReadings[0]
	s.mu.Unlock()

	if upserted.ID != existingID {
		t.Errorf("upserted reading.ID = %q, want existing id %q (force must overwrite the same row, not create a new one)", upserted.ID, existingID)
	}
	if upserted.ContentSHA256 != "new-sha" {
		t.Errorf("upserted reading.ContentSHA256 = %q, want %q (sha must reflect latest fetch)", upserted.ContentSHA256, "new-sha")
	}

	if len(mockOut.readingSaves) != 1 {
		t.Fatalf("SaveReadingContent calls = %d, want 1", len(mockOut.readingSaves))
	}
	if mockOut.readingSaves[0].readingID != existingID {
		t.Errorf("SaveReadingContent readingID = %q, want %q (on-disk persist must target the existing id so the API can find it)",
			mockOut.readingSaves[0].readingID, existingID)
	}
}

// TestHandleCompletion_ReadDuplicate_Force_StillWrites verifies that a forced
// duplicate reading still writes (upserts) to refresh the reading row.
func TestHandleCompletion_ReadDuplicate_Force_StillWrites(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_dup_force",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Force:       true,
		Prompt:      "https://example.com/post",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.5}}
	o := newTestOrchestrator(s, bus, withEmbedder(emb))
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_dup_force")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:           true,
		Complete:       true,
		TaskMode:       models.TaskModeRead,
		URL:            "https://example.com/post",
		TLDR:           "refreshed summary",
		NoveltyVerdict: "duplicate",
	})
	bus.Close()

	s.mu.Lock()
	if len(s.upsertedReadings) != 1 {
		t.Fatalf("upsertedReadings = %d, want 1 for forced duplicate", len(s.upsertedReadings))
	}
	s.mu.Unlock()
}

func TestHandleCompletion_ReadStoreFailure_MarksTaskFailed(t *testing.T) {
	s := newMockStore()
	s.upsertReadingErr = fmt.Errorf("db write failed")
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_store_fail",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "https://example.com/post",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.1}}
	o := newTestOrchestrator(s, bus, withEmbedder(emb))
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_store_fail")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		TaskMode: models.TaskModeRead,
		URL:      "https://example.com/post",
		TLDR:     "short summary",
	})
	bus.Close()

	got, _ := s.GetTask(context.Background(), "bf_read_store_fail")
	if got.Status != models.TaskStatusFailed {
		t.Errorf("task.Status = %q, want failed", got.Status)
	}

	events := n.eventTypes()
	if len(events) != 1 || events[0] != notify.EventTaskFailed {
		t.Errorf("events = %v, want [task.failed]", events)
	}
}

func TestHandleCompletion_ReadEmbedFailure_MarksTaskFailed(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_embed_fail",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Prompt:      "https://example.com/post",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	emb := &mockEmbedder{errToFn: fmt.Errorf("openai down")}
	o := newTestOrchestrator(s, bus, withEmbedder(emb))
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_embed_fail")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		TaskMode: models.TaskModeRead,
		URL:      "https://example.com/post",
		TLDR:     "short summary",
	})
	bus.Close()

	got, _ := s.GetTask(context.Background(), "bf_read_embed_fail")
	if got.Status != models.TaskStatusFailed {
		t.Errorf("task.Status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("expected non-empty task.Error when embedding fails")
	}

	s.mu.Lock()
	if len(s.upsertedReadings) != 0 {
		t.Errorf("reading written on embed failure: upserted=%d", len(s.upsertedReadings))
	}
	s.mu.Unlock()

	events := n.eventTypes()
	if len(events) != 1 || events[0] != notify.EventTaskFailed {
		t.Errorf("events = %v, want [task.failed]", events)
	}
}

func TestHandleCompletion_ReadForce_CallsUpsertReading(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_read_force",
		Status:      models.TaskStatusRunning,
		TaskMode:    models.TaskModeRead,
		Force:       true,
		Prompt:      "https://example.com/post",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	emb := &mockEmbedder{vector: []float32{0.9}}
	o := newTestOrchestrator(s, bus, withEmbedder(emb))
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_read_force")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		Complete: true,
		TaskMode: models.TaskModeRead,
		URL:      "https://example.com/post",
		TLDR:     "updated summary",
	})
	bus.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.upsertedReadings) != 1 {
		t.Fatalf("upsertedReadings = %d, want 1", len(s.upsertedReadings))
	}
	if s.upsertedReadings[0].URL != "https://example.com/post" {
		t.Errorf("upserted URL = %q", s.upsertedReadings[0].URL)
	}
}

func TestKillTask(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_kill",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "long running task",
		ContainerID: "cont_kill",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_kill")
	o.killTask(context.Background(), task, "exceeded max runtime")
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_kill")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if task.Error != "exceeded max runtime" {
		t.Errorf("error = %q, want 'exceeded max runtime'", task.Error)
	}
	if task.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", types)
	}

	// After kill, task should be marked ready for retry (under cap)
	if !task.ReadyForRetry {
		t.Error("expected ReadyForRetry=true after kill (under retry cap)")
	}
}

// TestKillTask_CompleteTaskError pins that a write failure during killTask
// keeps the slot held and suppresses the event. The DB row stays running, so
// the next monitor tick will reprocess the (now-stopped) container and try
// Complete again. Releasing the slot or emitting here would lie about a
// terminal state we never persisted.
func TestKillTask_CompleteTaskError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_kill_err",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "task that fails to complete",
		ContainerID: "cont_kill_err",
		StartedAt:   &now,
	})

	s.completeTaskErr = fmt.Errorf("db connection lost")

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_kill_err")

	// Should not panic even when CompleteTask fails.
	o.killTask(context.Background(), task, "exceeded max runtime")
	bus.Close()

	if o.running != 1 {
		t.Errorf("running = %d, want 1 (slot must stay held when DB write failed)", o.running)
	}
	if evTypes := n.eventTypes(); len(evTypes) != 0 {
		t.Errorf("events = %v, want none on write failure", evTypes)
	}
}

func TestRequeueTask_LocalMode(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_requeue_local",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "requeue me",
		ContainerID: "cont_rq",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_requeue_local")
	o.requeueTask(context.Background(), task, "container gone")

	task, _ = s.GetTask(context.Background(), "bf_requeue_local")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	if task.ContainerID != "" {
		t.Errorf("container ID = %q, want cleared", task.ContainerID)
	}
}

// --- monitorRunning integration tests ---

func TestMonitorRunning_TimedOutTaskKilled(t *testing.T) {
	s := newMockStore()
	past := time.Now().UTC().Add(-60 * time.Minute)
	s.CreateTask(context.Background(), &models.Task{
		ID:            "bf_timeout",
		Status:        models.TaskStatusRunning,
		RepoURL:       "https://github.com/test/repo",
		Prompt:        "long task",
		ContainerID:   "cont1",
		StartedAt:     &past,
		MaxRuntimeSec: 600,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1

	o.monitorRunning(context.Background())

	task, _ := s.GetTask(context.Background(), "bf_timeout")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

func TestMonitorRunning_StillRunning(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_still",
		Status:      models.TaskStatusRunning,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"cont1": {Done: false},
		},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1

	o.monitorRunning(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_still")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", task.Status)
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
	if len(n.eventTypes()) != 0 {
		t.Errorf("expected no events, got %d", len(n.eventTypes()))
	}
}

func TestMonitorRunning_Completed(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_done",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "finish me",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"cont1": {Done: true, Complete: true, ExitCode: 0, PRURL: "https://github.com/test/repo/pull/42"},
		},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1

	o.monitorRunning(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_done")
	if task.Status != models.TaskStatusCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCompleted {
		t.Errorf("expected [task.completed], got %v", types)
	}
}

func TestMonitorRunning_Completed_PersistsFinalOutputMetadata(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:              "bf_done_meta",
		Status:          models.TaskStatusRunning,
		RepoURL:         "https://github.com/test/repo",
		Prompt:          "finish me",
		SaveAgentOutput: true,
		ContainerID:     "cont1",
		StartedAt:       &now,
		CreatedAt:       now,
	})

	bus, _ := newTestBus()
	root := t.TempDir()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"cont1": {
				Done:         true,
				Complete:     true,
				ExitCode:     0,
				PRURL:        "https://github.com/test/repo/pull/42",
				RepoURL:      "https://github.com/test/repo",
				TargetBranch: "main",
				TaskMode:     "code",
			},
		},
		agentOutput: "agent log bytes",
	}
	o := newTestOrchestrator(s, bus, withDocker(mock), withOutputs(outputs.New(root)))
	o.running = 1

	o.monitorRunning(context.Background())
	bus.Close()

	data, err := os.ReadFile(filepath.Join(root, "tasks", "bf_done_meta", "task.json"))
	if err != nil {
		t.Fatalf("read task.json: %v", err)
	}

	var meta taskMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal task.json: %v (body: %s)", err, string(data))
	}
	if meta.Status != models.TaskStatusCompleted {
		t.Errorf("status = %q, want %q", meta.Status, models.TaskStatusCompleted)
	}
	if meta.PRURL != "https://github.com/test/repo/pull/42" {
		t.Errorf("PRURL = %q, want PR URL", meta.PRURL)
	}
	if meta.OutputURL != "/api/v1/tasks/bf_done_meta/output" {
		t.Errorf("OutputURL = %q, want output endpoint", meta.OutputURL)
	}
	if meta.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
}

// TestMonitorRunning_SaveMetadataRunsAfterCompleteTask is a regression anchor
// for the completion-artifact ordering invariant: task.json must reflect the
// final committed row, not a stale "running" snapshot.
//
// The orchestrator deliberately splits SaveLog and SaveMetadata so that
// SaveMetadata runs AFTER CompleteTask + GetTask reloads the finished row. If
// a future refactor fuses the two back together (or moves SaveMetadata before
// the DB update), task.json will pin the task as running forever with no
// CompletedAt — exactly what this test guards against.
func TestMonitorRunning_SaveMetadataRunsAfterCompleteTask(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:              "bf_order",
		Status:          models.TaskStatusRunning,
		RepoURL:         "https://github.com/test/repo",
		Prompt:          "finish me",
		SaveAgentOutput: true,
		ContainerID:     "cont1",
		StartedAt:       &now,
		CreatedAt:       now,
	})

	bus, _ := newTestBus()
	root := t.TempDir()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"cont1": {
				Done:         true,
				Complete:     true,
				ExitCode:     0,
				PRURL:        "https://github.com/test/repo/pull/7",
				RepoURL:      "https://github.com/test/repo",
				TargetBranch: "main",
				TaskMode:     "code",
			},
		},
		agentOutput: "final agent bytes",
	}
	o := newTestOrchestrator(s, bus, withDocker(mock), withOutputs(outputs.New(root)))
	o.running = 1

	o.monitorRunning(context.Background())
	bus.Close()

	data, err := os.ReadFile(filepath.Join(root, "tasks", "bf_order", "task.json"))
	if err != nil {
		t.Fatalf("read task.json: %v", err)
	}
	var meta taskMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal task.json: %v (body: %s)", err, string(data))
	}

	// Primary fingerprint of a fused / pre-DB-write SaveMetadata: the snapshot
	// still reports running with no CompletedAt.
	if meta.Status == models.TaskStatusRunning {
		t.Fatalf("task.json captured stale running snapshot (status=%q) — SaveMetadata must run after CompleteTask", meta.Status)
	}
	if meta.CompletedAt == nil {
		t.Fatal("task.json has nil CompletedAt — SaveMetadata ran before CompleteTask populated completed_at")
	}
	if meta.Status != models.TaskStatusCompleted {
		t.Errorf("status = %q, want %q", meta.Status, models.TaskStatusCompleted)
	}
	if meta.PRURL != "https://github.com/test/repo/pull/7" {
		t.Errorf("PRURL = %q, want populated from completed row", meta.PRURL)
	}
	if meta.OutputURL != "/api/v1/tasks/bf_order/output" {
		t.Errorf("OutputURL = %q, want output endpoint", meta.OutputURL)
	}
}

func TestMonitorRunning_CompletedFromStatusFile(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_done_status",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "finish me",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"cont1": {Done: true, Complete: true, ExitCode: 1, PRURL: "https://github.com/test/repo/pull/43"},
		},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1

	o.monitorRunning(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_done_status")
	if task.Status != models.TaskStatusCompleted {
		t.Errorf("status = %q, want completed", task.Status)
	}
	if task.Error != "" {
		t.Errorf("error = %q, want empty", task.Error)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCompleted {
		t.Errorf("expected [task.completed], got %v", types)
	}
}

func TestMonitorRunning_InspectError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_ierr",
		Status:      models.TaskStatusRunning,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		inspectErrors: map[string]error{
			"cont1": fmt.Errorf("connection refused"),
		},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1

	o.monitorRunning(context.Background())

	// After 1 failure, task should still be running (not killed yet)
	task, _ := s.GetTask(context.Background(), "bf_ierr")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running (only 1 failure)", task.Status)
	}
	if o.inspectFailures["bf_ierr"] != 1 {
		t.Errorf("inspectFailures = %d, want 1", o.inspectFailures["bf_ierr"])
	}
}

func TestMonitorRunning_ClearsInspectFailuresOnSuccess(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_clear",
		Status:      models.TaskStatusRunning,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"cont1": {Done: false},
		},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1
	o.inspectFailures["bf_clear"] = 2 // had prior failures

	o.monitorRunning(context.Background())

	if _, exists := o.inspectFailures["bf_clear"]; exists {
		t.Error("inspectFailures should be cleared on successful inspect")
	}
}

// --- handleInspectError tests ---

func TestHandleInspectError_AccumulatesFailures(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_accum",
		Status:      models.TaskStatusRunning,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_accum")

	// First failure
	o.handleInspectError(context.Background(), task, fmt.Errorf("connection refused"))
	if o.inspectFailures["bf_accum"] != 1 {
		t.Fatalf("after 1st failure: count = %d, want 1", o.inspectFailures["bf_accum"])
	}

	// Second failure
	o.handleInspectError(context.Background(), task, fmt.Errorf("connection refused"))
	if o.inspectFailures["bf_accum"] != 2 {
		t.Fatalf("after 2nd failure: count = %d, want 2", o.inspectFailures["bf_accum"])
	}

	// Task should still be running
	task, _ = s.GetTask(context.Background(), "bf_accum")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running (under threshold)", task.Status)
	}
}

func TestHandleInspectError_KillsAtMaxFailures(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_maxfail",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "failing task",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1
	o.inspectFailures["bf_maxfail"] = maxInspectFailures - 1 // one away from max

	task, _ := s.GetTask(context.Background(), "bf_maxfail")
	o.handleInspectError(context.Background(), task, fmt.Errorf("connection refused"))
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_maxfail")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	if _, exists := o.inspectFailures["bf_maxfail"]; exists {
		t.Error("inspectFailures should be cleared after kill")
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", types)
	}
}

func TestTaskMetadata_JSONSerialization(t *testing.T) {
	now := time.Now().UTC()
	meta := taskMetadata{
		ID:            "bf_ser",
		Status:        models.TaskStatusCompleted,
		TaskMode:      "code",
		Harness:       models.HarnessClaudeCode,
		RepoURL:       "https://github.com/test/repo",
		Branch:        "feature",
		Prompt:        "implement feature",
		Effort:        "high",
		MaxBudgetUSD:  5.0,
		MaxTurns:      50,
		MaxRuntimeSec: 1800,
		CreatePR:      true,
		SelfReview:    true,
		PRURL:         "https://github.com/test/repo/pull/5",
		CreatedAt:     now,
		CompletedAt:   &now,
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}

	var decoded taskMetadata
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal metadata: %v", err)
	}

	if decoded.ID != "bf_ser" {
		t.Errorf("ID = %q, want bf_ser", decoded.ID)
	}
	if decoded.Status != models.TaskStatusCompleted {
		t.Errorf("Status = %q, want completed", decoded.Status)
	}
	if decoded.PRURL != "https://github.com/test/repo/pull/5" {
		t.Errorf("PRURL = %q, want PR URL", decoded.PRURL)
	}
	if !decoded.CreatePR {
		t.Error("CreatePR should be true")
	}
	if !decoded.SelfReview {
		t.Error("SelfReview should be true")
	}
	if decoded.Effort != "high" {
		t.Errorf("Effort = %q, want high", decoded.Effort)
	}
	if decoded.MaxBudgetUSD != 5.0 {
		t.Errorf("MaxBudgetUSD = %v, want 5.0", decoded.MaxBudgetUSD)
	}
	if decoded.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", decoded.MaxTurns)
	}
	if decoded.MaxRuntimeSec != 1800 {
		t.Errorf("MaxRuntimeSec = %d, want 1800", decoded.MaxRuntimeSec)
	}
}

func TestIsTimedOut(t *testing.T) {
	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(newMockStore(), bus)

	// No StartedAt — not timed out
	task := &models.Task{MaxRuntimeSec: 600}
	if o.isTimedOut(task) {
		t.Error("task without StartedAt should not be timed out")
	}

	// MaxRuntimeSec = 0 — no timeout
	now := time.Now().UTC()
	task = &models.Task{StartedAt: &now, MaxRuntimeSec: 0}
	if o.isTimedOut(task) {
		t.Error("task with MaxRuntimeSec=0 should not be timed out")
	}

	// Recently started — not timed out
	task = &models.Task{StartedAt: &now, MaxRuntimeSec: 600}
	if o.isTimedOut(task) {
		t.Error("recently started task should not be timed out")
	}

	// Started long ago — timed out
	past := time.Now().UTC().Add(-20 * time.Minute)
	task = &models.Task{StartedAt: &past, MaxRuntimeSec: 600}
	if !o.isTimedOut(task) {
		t.Error("task past deadline should be timed out")
	}
}

// --- Error resilience tests (P0 #7) ---

func TestMonitorCancelled_StopContainerError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_stop_err",
		Status:      models.TaskStatusCancelled,
		ContainerID: "abc123",
		StartedAt:   &now,
		CompletedAt: &now,
	})

	bus, notifier := newTestBus()
	mock := &mockDockerManager{
		stopContainerErr: fmt.Errorf("connection refused"),
		inspectResults:   map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1

	o.monitorCancelled(context.Background())
	bus.Close()

	// ClearTaskAssignment should still run despite StopContainer error
	task, _ := s.GetTask(context.Background(), "bf_stop_err")
	// markRetryReady should still run
	if !task.ReadyForRetry {
		t.Error("ReadyForRetry should be true (markRetryReady should still run)")
	}

	// Event should still be emitted
	types := notifier.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCancelled {
		t.Errorf("expected [task.cancelled], got %v", types)
	}

	// Running should be decremented
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

func TestMonitorCancelled_ClearAssignmentError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_clear_err",
		Status:      models.TaskStatusCancelled,
		ContainerID: "abc123",
		StartedAt:   &now,
		CompletedAt: &now,
	})

	s.clearTaskAssignmentErr = fmt.Errorf("db connection lost")

	bus, notifier := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	o.monitorCancelled(context.Background())
	bus.Close()

	// markRetryReady should still run despite ClearTaskAssignment error
	task, _ := s.GetTask(context.Background(), "bf_clear_err")
	if !task.ReadyForRetry {
		t.Error("ReadyForRetry should be true (markRetryReady should still run)")
	}

	// Event should still be emitted
	types := notifier.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCancelled {
		t.Errorf("expected [task.cancelled], got %v", types)
	}
}

// TestHandleCompletion_CompleteTaskError pins that a CompleteTask write
// failure keeps the slot held and suppresses task.completed. The container
// has already exited and the DB row is still running, so the next monitor
// tick reprocesses the container and retries — emitting here would let the
// next tick double-emit task.completed.
func TestHandleCompletion_CompleteTaskError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_comp_err",
		Status:      models.TaskStatusRunning,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	s.completeTaskErr = fmt.Errorf("db write failed")

	bus, notifier := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_comp_err")
	status := ContainerStatus{Done: true, ExitCode: 0, Complete: true}
	o.handleCompletion(context.Background(), task, status)
	bus.Close()

	if o.running != 1 {
		t.Errorf("running = %d, want 1 (slot stays held on write failure)", o.running)
	}
	if types := notifier.eventTypes(); len(types) != 0 {
		t.Errorf("events = %v, want none on write failure", types)
	}
}

func TestKillTask_StopContainerError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_kill_err",
		Status:      models.TaskStatusRunning,
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, notifier := newTestBus()
	mock := &mockDockerManager{
		stopContainerErr: fmt.Errorf("timeout stopping container"),
		inspectResults:   map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_kill_err")
	o.killTask(context.Background(), task, "test kill")
	bus.Close()

	// CompleteTask should still run
	task, _ = s.GetTask(context.Background(), "bf_kill_err")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed (CompleteTask should still run)", task.Status)
	}

	// releaseSlot should still run
	if o.running != 0 {
		t.Errorf("running = %d, want 0 (releaseSlot should still run)", o.running)
	}

	// Event should still be emitted
	types := notifier.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", types)
	}
}

// --- saveAgentOutput (filesystem writer) ---

func TestSaveAgentOutput_WritesViaWriter(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:              "bf_fs_out",
		Status:          models.TaskStatusRunning,
		SaveAgentOutput: true,
		ContainerID:     "cont1",
		StartedAt:       &now,
		CreatedAt:       now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	writer := &mockWriter{}
	docker := &mockDockerManager{agentOutput: "agent log bytes"}
	o := newTestOrchestrator(s, bus, withDocker(docker), withOutputs(writer))

	task, _ := s.GetTask(context.Background(), "bf_fs_out")
	o.saveAgentOutput(context.Background(), task)

	if len(writer.logSaves) != 1 {
		t.Fatalf("expected 1 writer.SaveLog call, got %d", len(writer.logSaves))
	}
	save := writer.logSaves[0]
	if save.taskID != "bf_fs_out" {
		t.Errorf("taskID = %q, want %q", save.taskID, "bf_fs_out")
	}
	if string(save.log) != "agent log bytes" {
		t.Errorf("log = %q, want %q", string(save.log), "agent log bytes")
	}
	if len(writer.metadataSaves) != 0 {
		t.Fatalf("expected 0 writer.SaveMetadata calls, got %d", len(writer.metadataSaves))
	}

	if task.OutputURL != "/api/v1/tasks/bf_fs_out/output" {
		t.Errorf("task.OutputURL = %q, want %q", task.OutputURL, "/api/v1/tasks/bf_fs_out/output")
	}
}

func TestSaveAgentOutput_NilWriter(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:              "bf_fs_nil",
		Status:          models.TaskStatusRunning,
		SaveAgentOutput: true,
		ContainerID:     "cont1",
		StartedAt:       &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	// No outputs writer configured — should be a silent no-op.
	o := newTestOrchestrator(s, bus)

	task, _ := s.GetTask(context.Background(), "bf_fs_nil")
	o.saveAgentOutput(context.Background(), task)

	if task.OutputURL != "" {
		t.Errorf("task.OutputURL = %q, want empty when writer is nil", task.OutputURL)
	}
}

func TestSaveAgentOutput_GateOffWhenSaveDisabled(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:              "bf_fs_gate",
		Status:          models.TaskStatusRunning,
		SaveAgentOutput: false, // gated off
		ContainerID:     "cont1",
		StartedAt:       &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	writer := &mockWriter{}
	docker := &mockDockerManager{agentOutput: "never written"}
	o := newTestOrchestrator(s, bus, withDocker(docker), withOutputs(writer))

	task, _ := s.GetTask(context.Background(), "bf_fs_gate")
	o.saveAgentOutput(context.Background(), task)

	if len(writer.logSaves) != 0 {
		t.Errorf("expected 0 SaveLog calls (SaveAgentOutput=false), got %d", len(writer.logSaves))
	}
	if task.OutputURL != "" {
		t.Errorf("task.OutputURL = %q, want empty when save gated off", task.OutputURL)
	}
}

func TestParseCapturedSidecar(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		wantNil bool
		check   func(t *testing.T, c *capturedContent)
	}{
		{
			name:    "nil bytes",
			input:   nil,
			wantNil: true,
		},
		{
			name:    "empty bytes",
			input:   []byte{},
			wantNil: true,
		},
		{
			name:    "malformed JSON",
			input:   []byte(`{not json`),
			wantNil: true,
		},
		{
			name:    "wrong type for content_bytes",
			input:   []byte(`{"content_status":"captured","content_bytes":"not-a-number"}`),
			wantNil: true,
		},
		{
			name:    "missing content_status",
			input:   []byte(`{"url":"https://example.com","content_type":"text/html"}`),
			wantNil: true,
		},
		{
			name:    "empty content_status",
			input:   []byte(`{"url":"https://example.com","content_status":""}`),
			wantNil: true,
		},
		{
			name: "captured happy path",
			input: []byte(`{
				"url":"https://example.com",
				"content_type":"text/html; charset=utf-8",
				"content_status":"captured",
				"content_bytes":1024,
				"extracted_bytes":256,
				"content_sha256":"deadbeef",
				"fetched_at":"2026-04-25T12:00:00Z"
			}`),
			check: func(t *testing.T, c *capturedContent) {
				if c.URL != "https://example.com" {
					t.Errorf("URL = %q", c.URL)
				}
				if c.ContentType != "text/html; charset=utf-8" {
					t.Errorf("ContentType = %q", c.ContentType)
				}
				if c.ContentStatus != "captured" {
					t.Errorf("ContentStatus = %q", c.ContentStatus)
				}
				if c.ContentBytes != 1024 {
					t.Errorf("ContentBytes = %d", c.ContentBytes)
				}
				if c.ExtractedBytes != 256 {
					t.Errorf("ExtractedBytes = %d", c.ExtractedBytes)
				}
				if c.ContentSHA256 != "deadbeef" {
					t.Errorf("ContentSHA256 = %q", c.ContentSHA256)
				}
				if c.FetchedAt != "2026-04-25T12:00:00Z" {
					t.Errorf("FetchedAt = %q", c.FetchedAt)
				}
			},
		},
		{
			name:  "non-captured status (e.g. fetch_failed) returned as-is",
			input: []byte(`{"content_status":"fetch_failed","content_type":""}`),
			check: func(t *testing.T, c *capturedContent) {
				// Forward-compat: future failure-mode statuses must round-trip
				// through the parser so the row reflects what the agent wrote.
				if c.ContentStatus != "fetch_failed" {
					t.Errorf("ContentStatus = %q, want fetch_failed", c.ContentStatus)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCapturedSidecar(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want non-nil capturedContent")
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}
