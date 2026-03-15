package orchestrator

import (
	"context"
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
	o.running = 1

	o.monitorCancelled(context.Background())

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
}

func TestMonitorCancelled_IgnoresWithoutContainer(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_cancel_prov",
		Status:      models.TaskStatusCancelled,
		CompletedAt: &now,
	})

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_ok")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		ExitCode: 0,
		PRURL:    "https://github.com/test/repo/pull/1",
	})

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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_input")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:       true,
		ExitCode:   1,
		NeedsInput: true,
		Question:   "What is the database password?",
	})

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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_fail")
	o.handleCompletion(context.Background(), task, ContainerStatus{
		Done:     true,
		ExitCode: 1,
		Error:    "something went wrong",
	})

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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_kill")
	o.killTask(context.Background(), task, "exceeded max runtime")

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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n, func(o *Orchestrator) {
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n) // defaults to ModeLocal
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
		MaxRuntimeMin: 10,
	})

	n := &mockNotifier{}
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))
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

	n := &mockNotifier{}
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont1": {Done: false},
		},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))
	o.running = 1

	o.monitorRunning(context.Background())

	task, _ := s.GetTask(context.Background(), "bf_still")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", task.Status)
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
	if len(n.events) != 0 {
		t.Errorf("expected no events, got %d", len(n.events))
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

	n := &mockNotifier{}
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont1": {Done: true, ExitCode: 0, PRURL: "https://github.com/test/repo/pull/42"},
		},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))
	o.running = 1

	o.monitorRunning(context.Background())

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

	n := &mockNotifier{}
	mock := &mockDockerManager{
		inspectErrors: map[string]error{
			"local/cont1": fmt.Errorf("connection refused"),
		},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))
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

	n := &mockNotifier{}
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont1": {Done: false},
		},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
	o.running = 1
	o.inspectFailures["bf_maxfail"] = maxInspectFailures - 1 // one away from max

	task, _ := s.GetTask(context.Background(), "bf_maxfail")
	o.handleInspectError(context.Background(), task, fmt.Errorf("connection refused"))

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

func TestIsTimedOut(t *testing.T) {
	o := newTestOrchestrator(newMockStore(), &mockNotifier{})

	// No StartedAt — not timed out
	task := &models.Task{MaxRuntimeMin: 10}
	if o.isTimedOut(task) {
		t.Error("task without StartedAt should not be timed out")
	}

	// MaxRuntimeMin = 0 — no timeout
	now := time.Now().UTC()
	task = &models.Task{StartedAt: &now, MaxRuntimeMin: 0}
	if o.isTimedOut(task) {
		t.Error("task with MaxRuntimeMin=0 should not be timed out")
	}

	// Recently started — not timed out
	task = &models.Task{StartedAt: &now, MaxRuntimeMin: 10}
	if o.isTimedOut(task) {
		t.Error("recently started task should not be timed out")
	}

	// Started long ago — timed out
	past := time.Now().UTC().Add(-20 * time.Minute)
	task = &models.Task{StartedAt: &past, MaxRuntimeMin: 10}
	if !o.isTimedOut(task) {
		t.Error("task past deadline should be timed out")
	}
}
