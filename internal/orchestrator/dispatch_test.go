package orchestrator

import (
	"context"
	"fmt"
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
		RepoURL:    "https://github.com/test/repo",
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
