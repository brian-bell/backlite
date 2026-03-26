package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
)

func TestMonitorCancelled_DecrementsRunning(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_cancel_run",
		Status:      models.TaskStatusCancelled,
		InstanceID:  "local",
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

	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0", inst.RunningContainers)
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

func TestMonitorCancelled_IgnoresWithoutContainer(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_cancel_prov",
		Status:      models.TaskStatusCancelled,
		CompletedAt: &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 0

	o.monitorCancelled(context.Background())

	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

func TestMonitorCancelled_RecoveringTaskCancelled(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_cancel_recov",
		Status:      models.TaskStatusCancelled,
		InstanceID:  "local",
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

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_ok",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_ok")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
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

	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0", inst.RunningContainers)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskCompleted {
		t.Errorf("expected [task.completed], got %v", types)
	}
}

func TestHandleCompletion_CompleteFlagOverridesExitCode(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_complete_flag",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		InstanceID:  "local",
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

func TestHandleCompletion_NeedsInput(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_input",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		InstanceID:  "local",
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

	s.CreateInstance(context.Background(), newLocalInstance())

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_fail",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "do something",
		InstanceID:  "local",
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
}

func TestHandleCompletion_PropagatesInferredFields(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_inferred",
		Status:      models.TaskStatusRunning,
		TaskMode:    "auto",
		Prompt:      "fix the bug in https://github.com/test/repo",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
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
}

func TestKillTask(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_kill",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "long running task",
		InstanceID:  "local",
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

	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0", inst.RunningContainers)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", types)
	}
}

func TestKillTask_CompleteTaskError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_kill_err",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "task that fails to complete",
		InstanceID:  "local",
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

	// releaseSlot should still run.
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	// Event should still be emitted.
	evTypes := n.eventTypes()
	if len(evTypes) != 1 || evTypes[0] != notify.EventTaskFailed {
		t.Errorf("expected [task.failed], got %v", evTypes)
	}
}

func TestRequeueTask_EC2Mode(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "i-abc",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_requeue",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "requeue me",
		InstanceID:  "i-abc",
		ContainerID: "cont_rq",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus, func(o *Orchestrator) {
		o.config.Mode = config.ModeEC2
	})
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_requeue")
	o.requeueTask(context.Background(), task, "instance terminated")

	task, _ = s.GetTask(context.Background(), "bf_requeue")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.InstanceID != "" {
		t.Errorf("instanceID = %q, want empty", task.InstanceID)
	}
	if task.ContainerID != "" {
		t.Errorf("containerID = %q, want empty", task.ContainerID)
	}
	if task.StartedAt != nil {
		t.Error("StartedAt should be nil")
	}
	if task.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", task.RetryCount)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	// Instance should be marked terminated in EC2 mode
	inst, _ := s.GetInstance(context.Background(), "i-abc")
	if inst.Status != models.InstanceStatusTerminated {
		t.Errorf("instance status = %q, want terminated", inst.Status)
	}
}

func TestRequeueTask_LocalMode(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_requeue_local",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "requeue me",
		InstanceID:  "local",
		ContainerID: "cont_rq",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus) // defaults to ModeLocal
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

	// Instance should NOT be terminated in local mode
	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.Status != models.InstanceStatusRunning {
		t.Errorf("instance status = %q, want running (local mode should not terminate)", inst.Status)
	}
}

// --- monitorRunning integration tests ---

func TestMonitorRunning_TimedOutTaskKilled(t *testing.T) {
	s := newMockStore()
	past := time.Now().UTC().Add(-60 * time.Minute)

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})
	s.CreateTask(context.Background(), &models.Task{
		ID:            "bf_timeout",
		Status:        models.TaskStatusRunning,
		RepoURL:       "https://github.com/test/repo",
		Prompt:        "long task",
		InstanceID:    "local",
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

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_still",
		Status:      models.TaskStatusRunning,
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont1": {Done: false},
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

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_done",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "finish me",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont1": {Done: true, ExitCode: 0, PRURL: "https://github.com/test/repo/pull/42"},
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

func TestMonitorRunning_CompletedFromStatusFile(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_done_status",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "finish me",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont1": {Done: true, Complete: true, ExitCode: 1, PRURL: "https://github.com/test/repo/pull/43"},
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

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_ierr",
		Status:      models.TaskStatusRunning,
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		inspectErrors: map[string]error{
			"local/cont1": fmt.Errorf("connection refused"),
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

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_clear",
		Status:      models.TaskStatusRunning,
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont1": {Done: false},
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

func TestHandleInspectError_InstanceGone(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_gone",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "lost instance",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1
	o.inspectFailures["bf_gone"] = 2 // should be cleared

	task, _ := s.GetTask(context.Background(), "bf_gone")
	o.handleInspectError(context.Background(), task, fmt.Errorf("InvalidInstanceId: i-abc does not exist"))

	task, _ = s.GetTask(context.Background(), "bf_gone")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending (requeued)", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", task.RetryCount)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	if _, exists := o.inspectFailures["bf_gone"]; exists {
		t.Error("inspectFailures should be cleared on instance gone")
	}
}

func TestHandleInspectError_InstanceGone_FargatePreservesSyntheticInstance(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "fargate",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     5,
		RunningContainers: 1,
	})
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_gone_fargate",
		Status:      models.TaskStatusRunning,
		InstanceID:  "fargate",
		ContainerID: "arn:aws:ecs:us-east-1:123456789012:task/backflow/abc",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.config.Mode = config.ModeFargate
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_gone_fargate")
	o.handleInspectError(context.Background(), task, fmt.Errorf("%w: Fargate Spot capacity reclaimed", ErrSpotInterruption))

	inst, _ := s.GetInstance(context.Background(), "fargate")
	if inst.Status != models.InstanceStatusRunning {
		t.Errorf("instance status = %q, want running", inst.Status)
	}
}

func TestHandleInspectError_AccumulatesFailures(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_accum",
		Status:      models.TaskStatusRunning,
		InstanceID:  "local",
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

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_maxfail",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "failing task",
		InstanceID:  "local",
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

func TestSaveTaskMetadata_NilS3(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_meta_nil",
		Status:      models.TaskStatusCompleted,
		TaskMode:    "code",
		Harness:     models.HarnessClaudeCode,
		RepoURL:     "https://github.com/test/repo",
		Branch:      "main",
		Prompt:      "do something",
		CreatePR:    true,
		PRURL:       "https://github.com/test/repo/pull/1",
		CreatedAt:   now,
		CompletedAt: &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	// o.s3 is nil — should be a no-op without panicking
	task, _ := s.GetTask(context.Background(), "bf_meta_nil")
	o.saveTaskMetadata(context.Background(), task)
}

func TestSaveTaskMetadata_JSONSerialization(t *testing.T) {
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

func TestSaveTaskMetadata_UploadIntegration(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()
	started := now.Add(-5 * time.Minute)

	task := &models.Task{
		ID:            "bf_meta_int",
		Status:        models.TaskStatusCompleted,
		TaskMode:      "code",
		Harness:       models.HarnessClaudeCode,
		RepoURL:       "https://github.com/test/repo",
		Branch:        "feature",
		TargetBranch:  "main",
		Prompt:        "implement feature",
		Model:         "claude-sonnet-4-20250514",
		Effort:        "high",
		MaxBudgetUSD:  5.0,
		MaxTurns:      50,
		MaxRuntimeSec: 1800,
		CreatePR:      true,
		SelfReview:    true,
		PRURL:         "https://github.com/test/repo/pull/10",
		CostUSD:       1.23,
		RetryCount:    1,
		CreatedAt:     now,
		StartedAt:     &started,
		CompletedAt:   &now,
	}
	s.CreateTask(context.Background(), task)

	mockS3 := &mockS3Client{}
	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus, withS3(mockS3))

	stored, _ := s.GetTask(context.Background(), "bf_meta_int")
	o.saveTaskMetadata(context.Background(), stored)

	if len(mockS3.uploads) != 1 {
		t.Fatalf("expected 1 upload, got %d", len(mockS3.uploads))
	}

	upload := mockS3.uploads[0]

	expectedKey := "tasks/bf_meta_int/task_metadata.json"
	if upload.key != expectedKey {
		t.Errorf("S3 key = %q, want %q", upload.key, expectedKey)
	}

	var meta taskMetadata
	if err := json.Unmarshal(upload.data, &meta); err != nil {
		t.Fatalf("failed to unmarshal uploaded JSON: %v", err)
	}

	if meta.ID != "bf_meta_int" {
		t.Errorf("ID = %q, want bf_meta_int", meta.ID)
	}
	if meta.Status != models.TaskStatusCompleted {
		t.Errorf("Status = %q, want completed", meta.Status)
	}
	if meta.TaskMode != "code" {
		t.Errorf("TaskMode = %q, want code", meta.TaskMode)
	}
	if meta.Harness != models.HarnessClaudeCode {
		t.Errorf("Harness = %q, want claude_code", meta.Harness)
	}
	if meta.RepoURL != "https://github.com/test/repo" {
		t.Errorf("RepoURL = %q", meta.RepoURL)
	}
	if meta.Branch != "feature" {
		t.Errorf("Branch = %q, want feature", meta.Branch)
	}
	if meta.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want main", meta.TargetBranch)
	}
	if meta.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q", meta.Model)
	}
	if meta.Effort != "high" {
		t.Errorf("Effort = %q, want high", meta.Effort)
	}
	if meta.MaxBudgetUSD != 5.0 {
		t.Errorf("MaxBudgetUSD = %v, want 5.0", meta.MaxBudgetUSD)
	}
	if meta.MaxTurns != 50 {
		t.Errorf("MaxTurns = %d, want 50", meta.MaxTurns)
	}
	if meta.MaxRuntimeSec != 1800 {
		t.Errorf("MaxRuntimeSec = %d, want 1800", meta.MaxRuntimeSec)
	}
	if !meta.CreatePR {
		t.Error("CreatePR should be true")
	}
	if !meta.SelfReview {
		t.Error("SelfReview should be true")
	}
	if meta.PRURL != "https://github.com/test/repo/pull/10" {
		t.Errorf("PRURL = %q", meta.PRURL)
	}
	if meta.CostUSD != 1.23 {
		t.Errorf("CostUSD = %v, want 1.23", meta.CostUSD)
	}
	if meta.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", meta.RetryCount)
	}
}

func TestSaveTaskMetadata_UploadError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:        "bf_meta_err",
		Status:    models.TaskStatusCompleted,
		RepoURL:   "https://github.com/test/repo",
		Branch:    "main",
		Prompt:    "do something",
		CreatedAt: now,
	})

	mockS3 := &mockS3Client{err: fmt.Errorf("simulated S3 failure")}
	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus, withS3(mockS3))

	task, _ := s.GetTask(context.Background(), "bf_meta_err")

	// Should not panic on upload error — just logs and returns
	o.saveTaskMetadata(context.Background(), task)
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
