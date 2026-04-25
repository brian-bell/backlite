package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
)

func TestReleaseSlot_DecrementsRunningCounter(t *testing.T) {
	s := newMockStore()
	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 2

	o.releaseSlot(context.Background(), &models.Task{})

	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
}

func TestReleaseSlot_FloorsAtZero(t *testing.T) {
	s := newMockStore()
	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 0

	o.releaseSlot(context.Background(), &models.Task{})

	if o.running != 0 {
		t.Errorf("running = %d, want 0 (should not go negative)", o.running)
	}
}

// --- dispatchPending tests ---

func TestDispatchPending_NoCapacity(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_blocked",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "should not dispatch",
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus) // MaxContainers = 4
	o.running = 4                    // at capacity

	o.dispatchPending(context.Background())

	task, _ := s.GetTask(context.Background(), "bf_blocked")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending (no capacity)", task.Status)
	}
}

func TestDispatchPending_DispatchesTask(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_disp",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "dispatch me",
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		runAgentID:     "container-abc",
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))

	o.dispatchPending(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_disp")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", task.Status)
	}
	if task.ContainerID != "container-abc" {
		t.Errorf("containerID = %q, want container-abc", task.ContainerID)
	}
	if task.StartedAt == nil {
		t.Error("StartedAt should be set")
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRunning {
		t.Errorf("expected [task.running], got %v", types)
	}
}

// TestDispatch_RoutesToSkillAgentImage verifies dispatch consults the
// imagerouter and overrides task.AgentImage with the resolved value before
// calling docker.RunAgent. With BACKFLOW_SKILL_AGENT_IMAGE configured and a
// claude_code task, the skill image should win regardless of the value the
// task carried in (which would be the default agent image set at creation).
func TestDispatch_RoutesToSkillAgentImage(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:         "bf_skill",
		Status:     models.TaskStatusPending,
		Harness:    models.HarnessClaudeCode,
		TaskMode:   models.TaskModeCode,
		AgentImage: "backlite-agent",
		RepoURL:    "https://github.com/test/repo",
		Prompt:     "use the skill image",
	})

	var captured *models.Task
	bus, _ := newTestBus()
	mock := &mockDockerManager{
		runAgentFn: func(_ context.Context, task *models.Task) (string, error) {
			cp := *task
			captured = &cp
			return "container-skill", nil
		},
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.config.AgentImage = "backlite-agent"
	o.config.SkillAgentImage = "backlite-skill-agent:v1"

	task, _ := s.GetTask(context.Background(), "bf_skill")
	if err := o.dispatch(context.Background(), task); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	bus.Close()

	if captured == nil {
		t.Fatal("RunAgent was not called")
	}
	if captured.AgentImage != "backlite-skill-agent:v1" {
		t.Errorf("captured AgentImage = %q, want %q", captured.AgentImage, "backlite-skill-agent:v1")
	}
}

// TestDispatch_RoutesCodexToOldImage pins that codex tasks ignore the skill
// image even when BACKFLOW_SKILL_AGENT_IMAGE is set.
func TestDispatch_RoutesCodexToOldImage(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:         "bf_codex",
		Status:     models.TaskStatusPending,
		Harness:    models.HarnessCodex,
		TaskMode:   models.TaskModeCode,
		AgentImage: "backlite-agent",
		Prompt:     "codex task",
	})

	var captured *models.Task
	bus, _ := newTestBus()
	mock := &mockDockerManager{
		runAgentFn: func(_ context.Context, task *models.Task) (string, error) {
			cp := *task
			captured = &cp
			return "container-codex", nil
		},
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))
	o.config.AgentImage = "backlite-agent"
	o.config.SkillAgentImage = "backlite-skill-agent:v1"

	task, _ := s.GetTask(context.Background(), "bf_codex")
	if err := o.dispatch(context.Background(), task); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	bus.Close()

	if captured == nil {
		t.Fatal("RunAgent was not called")
	}
	if captured.AgentImage != "backlite-agent" {
		t.Errorf("captured AgentImage = %q, want %q (codex ignores skill image)", captured.AgentImage, "backlite-agent")
	}
}

func TestDispatchPending_FailedDispatch(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_dfail",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "fail to dispatch",
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		runAgentErr:    fmt.Errorf("docker daemon unavailable"),
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))

	o.dispatchPending(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_dfail")
	if task.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", task.Status)
	}
	if task.Error != "docker daemon unavailable" {
		t.Errorf("error = %q, want 'docker daemon unavailable'", task.Error)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", types)
	}
}

// --- dispatch tests ---

func TestDispatch_Success(t *testing.T) {
	s := newMockStore()
	task := &models.Task{
		ID:      "bf_dsuc",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "succeed",
	}
	s.CreateTask(context.Background(), task)

	bus, n := newTestBus()
	mock := &mockDockerManager{
		runAgentID:     "cont-xyz",
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))

	task, _ = s.GetTask(context.Background(), "bf_dsuc")
	err := o.dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_dsuc")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", task.Status)
	}
	if task.ContainerID != "cont-xyz" {
		t.Errorf("containerID = %q, want cont-xyz", task.ContainerID)
	}
	if task.StartedAt == nil {
		t.Error("StartedAt should be set")
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRunning {
		t.Errorf("expected [task.running], got %v", types)
	}
}

func TestDispatch_ReadTaskWithoutEmbedder_Fails(t *testing.T) {
	s := newMockStore()
	task := &models.Task{
		ID:       "bf_read_no_embedder",
		Status:   models.TaskStatusPending,
		TaskMode: models.TaskModeRead,
		Prompt:   "https://example.com/post",
	}
	s.CreateTask(context.Background(), task)

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		runAgentID:     "cont-should-not-run",
		inspectResults: map[string]ContainerStatus{},
	}
	// No embedder configured.
	o := newTestOrchestrator(s, bus, withDocker(mock))

	task, _ = s.GetTask(context.Background(), "bf_read_no_embedder")
	err := o.dispatch(context.Background(), task)
	if err == nil {
		t.Fatal("expected error from dispatch when read task has no embedder")
	}
	if !strings.Contains(err.Error(), "embedder") {
		t.Errorf("error = %q, want mention of embedder", err.Error())
	}

	got, _ := s.GetTask(context.Background(), "bf_read_no_embedder")
	if got.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty (no container should start)", got.ContainerID)
	}
	if got.Status == models.TaskStatusRunning {
		t.Errorf("task should not be running, got %q", got.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

// TestDispatch_ReadTask_OrchestratorMissingReaderImage_Fails ensures an
// orchestrator that doesn't have a reader image configured refuses to dispatch
// read tasks rather than silently running them on the default agent image.
// Protects against cross-orchestrator mis-dispatch in shared-DB setups.
//
// Uses Harness=Codex to isolate the ReaderImage path: SkillAgentImage is
// claude_code-only, so a codex read task with no ReaderImage is the only
// scenario where the guard must still fire even with SkillAgentImage set.
func TestDispatch_ReadTask_OrchestratorMissingReaderImage_Fails(t *testing.T) {
	s := newMockStore()
	task := &models.Task{
		ID:         "bf_read_no_reader",
		Status:     models.TaskStatusPending,
		TaskMode:   models.TaskModeRead,
		Harness:    models.HarnessCodex,
		Prompt:     "https://example.com/post",
		AgentImage: "backlite-reader", // set by the creating orchestrator
	}
	s.CreateTask(context.Background(), task)

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		runAgentID:     "cont-should-not-run",
		inspectResults: map[string]ContainerStatus{},
	}
	// ReaderImage is unset on this orchestrator — embedder set so we isolate
	// the reader-image guard.
	o := newTestOrchestrator(s, bus, withDocker(mock), withEmbedder(&mockEmbedder{}))

	task, _ = s.GetTask(context.Background(), "bf_read_no_reader")
	err := o.dispatch(context.Background(), task)
	if err == nil {
		t.Fatal("expected error from dispatch when orchestrator has no reader image")
	}
	if !strings.Contains(err.Error(), "BACKFLOW_READER_IMAGE") {
		t.Errorf("error = %q, want mention of BACKFLOW_READER_IMAGE", err.Error())
	}

	got, _ := s.GetTask(context.Background(), "bf_read_no_reader")
	if got.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty (no container should start)", got.ContainerID)
	}
	if got.Status == models.TaskStatusRunning {
		t.Errorf("task should not be running, got %q", got.Status)
	}
}

// TestDispatch_ReadTask_SkillImageWithoutReaderImage_Succeeds pins that an
// operator who configures BACKFLOW_SKILL_AGENT_IMAGE for a claude_code-only
// fleet can dispatch read tasks without separately configuring
// BACKFLOW_READER_IMAGE — the skill bundle ships the read skill, so the
// router resolves the skill image and dispatch must let it through.
func TestDispatch_ReadTask_SkillImageWithoutReaderImage_Succeeds(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:       "bf_read_skill_no_reader",
		Status:   models.TaskStatusPending,
		TaskMode: models.TaskModeRead,
		Harness:  models.HarnessClaudeCode,
		Prompt:   "https://example.com/post",
	})

	var captured *models.Task
	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		runAgentFn: func(_ context.Context, task *models.Task) (string, error) {
			cp := *task
			captured = &cp
			return "container-skill-read", nil
		},
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock), withEmbedder(&mockEmbedder{}))
	o.config.AgentImage = "backlite-agent"
	o.config.ReaderImage = ""                            // intentionally unset
	o.config.SkillAgentImage = "backlite-skill-agent:v1" // covers read via skill bundle

	task, _ := s.GetTask(context.Background(), "bf_read_skill_no_reader")
	if err := o.dispatch(context.Background(), task); err != nil {
		t.Fatalf("dispatch: %v (skill image should let read tasks through without ReaderImage)", err)
	}

	if captured == nil {
		t.Fatal("RunAgent was not called")
	}
	if captured.AgentImage != "backlite-skill-agent:v1" {
		t.Errorf("captured AgentImage = %q, want %q", captured.AgentImage, "backlite-skill-agent:v1")
	}
}

func TestDispatch_RunAgentError(t *testing.T) {
	s := newMockStore()
	task := &models.Task{
		ID:      "bf_derr",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "run agent fails",
	}
	s.CreateTask(context.Background(), task)

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		runAgentErr:    fmt.Errorf("image pull failed"),
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))

	task, _ = s.GetTask(context.Background(), "bf_derr")
	err := o.dispatch(context.Background(), task)
	if err == nil {
		t.Fatal("expected error from dispatch when RunAgent fails")
	}

	// Task should be in provisioning state (dispatch set it before RunAgent)
	task, _ = s.GetTask(context.Background(), "bf_derr")
	if task.Status != models.TaskStatusProvisioning {
		t.Errorf("status = %q, want provisioning (set before RunAgent call)", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0 (incrementRunning not called on failure)", o.running)
	}
}

// TestDispatch_ReadDuplicate_NonForce_FailsWithoutContainer verifies that a
// read-mode task for a URL already in the readings table short-circuits at
// dispatch time: no container launched, no embedding call, and a task.failed
// event with a duplicate-URL message is emitted.
func TestDispatch_ReadDuplicate_NonForce_FailsWithoutContainer(t *testing.T) {
	s := newMockStore()
	const url = "https://example.com/post"
	s.readingsByURL[url] = &models.Reading{
		ID:             "bf_existing_reading",
		URL:            url,
		Title:          "Previously captured",
		TLDR:           "older tldr",
		NoveltyVerdict: "novel",
	}
	task := &models.Task{
		ID:       "bf_read_dup_noforce",
		Status:   models.TaskStatusPending,
		TaskMode: models.TaskModeRead,
		Prompt:   url,
		Force:    false,
	}
	s.CreateTask(context.Background(), task)

	bus, n := newTestBus()
	mock := &mockDockerManager{
		runAgentID:     "cont-should-not-run",
		inspectResults: map[string]ContainerStatus{},
	}
	embedder := &mockEmbedder{}
	o := newTestOrchestrator(s, bus, withDocker(mock), withEmbedder(embedder))
	o.config.ReaderImage = "backlite-reader"

	task, _ = s.GetTask(context.Background(), "bf_read_dup_noforce")
	err := o.dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("dispatch returned error, want nil (inline-handled): %v", err)
	}
	bus.Close()

	got, _ := s.GetTask(context.Background(), "bf_read_dup_noforce")
	if got.Status != models.TaskStatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "reading already exists") {
		t.Errorf("task.Error = %q, want mention of 'reading already exists'", got.Error)
	}
	if !strings.Contains(got.Error, "bf_existing_reading") {
		t.Errorf("task.Error = %q, want mention of existing reading id", got.Error)
	}
	if got.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty (no container should launch)", got.ContainerID)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	if len(embedder.calls) != 0 {
		t.Errorf("embedder calls = %d, want 0", len(embedder.calls))
	}
	if len(s.upsertedReadings) != 0 {
		t.Errorf("reading writes happened: upserted=%d, want 0", len(s.upsertedReadings))
	}

	if len(n.events) != 1 {
		t.Fatalf("events = %d, want 1", len(n.events))
	}
	ev := n.events[0]
	if ev.Type != notify.EventTaskFailed {
		t.Errorf("event type = %q, want task.failed", ev.Type)
	}
	if !strings.Contains(ev.Message, "reading already exists") {
		t.Errorf("event.Message = %q, want duplicate-URL message", ev.Message)
	}
	if ev.TaskMode != models.TaskModeRead {
		t.Errorf("event.TaskMode = %q, want read", ev.TaskMode)
	}
}

// TestDispatch_ReadDuplicate_Force_ProceedsNormally verifies that Force=true
// bypasses the pre-dispatch duplicate check and launches the container.
func TestDispatch_ReadDuplicate_Force_ProceedsNormally(t *testing.T) {
	s := newMockStore()
	const url = "https://example.com/post"
	s.readingsByURL[url] = &models.Reading{
		ID:  "bf_existing_reading",
		URL: url,
	}
	task := &models.Task{
		ID:       "bf_read_dup_force",
		Status:   models.TaskStatusPending,
		TaskMode: models.TaskModeRead,
		Prompt:   url,
		Force:    true,
	}
	s.CreateTask(context.Background(), task)

	bus, n := newTestBus()
	mock := &mockDockerManager{
		runAgentID:     "cont-force",
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock), withEmbedder(&mockEmbedder{}))
	o.config.ReaderImage = "backlite-reader"

	task, _ = s.GetTask(context.Background(), "bf_read_dup_force")
	err := o.dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	bus.Close()

	got, _ := s.GetTask(context.Background(), "bf_read_dup_force")
	if got.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running (force bypasses dup check)", got.Status)
	}
	if got.ContainerID != "cont-force" {
		t.Errorf("ContainerID = %q, want cont-force", got.ContainerID)
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRunning {
		t.Errorf("expected [task.running], got %v", types)
	}
}

// TestDispatch_ReadDuplicate_LookupError_ReturnsError verifies that a DB error
// on the duplicate lookup is surfaced to dispatchPending so it marks the task
// failed via its generic error path, rather than silently proceeding.
func TestDispatch_ReadDuplicate_LookupError_ReturnsError(t *testing.T) {
	s := newMockStore()
	s.getReadingByURLErr = fmt.Errorf("db connection pool exhausted")

	task := &models.Task{
		ID:       "bf_read_dup_lookuperr",
		Status:   models.TaskStatusPending,
		TaskMode: models.TaskModeRead,
		Prompt:   "https://example.com/post",
		Force:    false,
	}
	s.CreateTask(context.Background(), task)

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		runAgentID:     "cont-should-not-run",
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock), withEmbedder(&mockEmbedder{}))
	o.config.ReaderImage = "backlite-reader"

	task, _ = s.GetTask(context.Background(), "bf_read_dup_lookuperr")
	err := o.dispatch(context.Background(), task)
	if err == nil {
		t.Fatal("expected error from dispatch when GetReadingByURL fails")
	}
	if !strings.Contains(err.Error(), "db connection pool exhausted") {
		t.Errorf("error = %q, want 'db connection pool exhausted'", err.Error())
	}
	got, _ := s.GetTask(context.Background(), "bf_read_dup_lookuperr")
	if got.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty (no container should launch)", got.ContainerID)
	}
}
