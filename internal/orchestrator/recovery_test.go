package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
)

func TestRecoverOnStartup_NoOrphans(t *testing.T) {
	s := newMockStore()
	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)

	o.recoverOnStartup(context.Background())
	bus.Close()

	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	if len(n.eventTypes()) != 0 {
		t.Errorf("expected no events, got %d", len(n.eventTypes()))
	}
}

func TestRecoverOnStartup_RunningOrphans(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_task1",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "fix bug",
		InstanceID:  "local",
		ContainerID: "abc123",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)

	o.recoverOnStartup(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_task1")
	if task.Status != models.TaskStatusRecovering {
		t.Errorf("task status = %q, want %q", task.Status, models.TaskStatusRecovering)
	}
	if task.ContainerID != "abc123" {
		t.Errorf("container ID should be preserved, got %q", task.ContainerID)
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 1 {
		t.Errorf("instance RunningContainers = %d, want 1", inst.RunningContainers)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRecovering {
		t.Errorf("expected [task.recovering], got %v", types)
	}
}

func TestRecoverOnStartup_ProvisioningOrphans(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:         "bf_task2",
		Status:     models.TaskStatusProvisioning,
		RepoURL:    "https://github.com/test/repo",
		Prompt:     "add feature",
		InstanceID: "i-12345",
	})

	bus, _ := newTestBus()
	o := newTestOrchestrator(s, bus)

	o.recoverOnStartup(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_task2")
	if task.Status != models.TaskStatusRecovering {
		t.Errorf("task status = %q, want %q", task.Status, models.TaskStatusRecovering)
	}
	if task.InstanceID != "" {
		t.Errorf("instance ID should be cleared, got %q", task.InstanceID)
	}
	if task.ContainerID != "" {
		t.Errorf("container ID should be cleared, got %q", task.ContainerID)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

func TestRecoverOnStartup_Mixed(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:    "local",
		Status:        models.InstanceStatusRunning,
		MaxContainers: 4,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_running",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "task 1",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})
	s.CreateTask(context.Background(), &models.Task{
		ID:         "bf_prov",
		Status:     models.TaskStatusProvisioning,
		RepoURL:    "https://github.com/test/repo",
		Prompt:     "task 2",
		InstanceID: "i-other",
	})

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)

	o.recoverOnStartup(context.Background())
	bus.Close()

	t1, _ := s.GetTask(context.Background(), "bf_running")
	t2, _ := s.GetTask(context.Background(), "bf_prov")
	if t1.Status != models.TaskStatusRecovering {
		t.Errorf("running task status = %q, want recovering", t1.Status)
	}
	if t2.Status != models.TaskStatusRecovering {
		t.Errorf("provisioning task status = %q, want recovering", t2.Status)
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
	if len(n.eventTypes()) != 2 {
		t.Errorf("expected 2 events, got %d", len(n.eventTypes()))
	}
}

func TestMonitorRecovering_NoContainer(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_noc",
		Status:  models.TaskStatusRecovering,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "orphaned provisioning task",
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)

	o.monitorRecovering(context.Background())

	task, _ := s.GetTask(context.Background(), "bf_noc")
	if task.Status != models.TaskStatusPending {
		t.Errorf("task status = %q, want pending", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", task.RetryCount)
	}
}

func TestMonitorRecovering_ContainerStillRunning(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_alive",
		Status:      models.TaskStatusRecovering,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "still running",
		InstanceID:  "local",
		ContainerID: "cont_alive",
		StartedAt:   &now,
	})

	bus, n := newTestBus()
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont_alive": {Done: false},
		},
	}
	o := newTestOrchestrator(s, bus, withDocker(mock))

	o.monitorRecovering(context.Background())
	bus.Close()

	task, _ := s.GetTask(context.Background(), "bf_alive")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("task status = %q, want running", task.Status)
	}
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRunning {
		t.Errorf("expected [task.running], got %v", types)
	}
}

func TestMonitorRecovering_ContainerExited(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_exited",
		Status:      models.TaskStatusRecovering,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "finished while down",
		InstanceID:  "local",
		ContainerID: "cont_exited",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_exited")
	status := ContainerStatus{Done: true, ExitCode: 0}
	o.handleCompletion(context.Background(), task, status)
	bus.Close()

	task, _ = s.GetTask(context.Background(), "bf_exited")
	if task.Status != models.TaskStatusCompleted {
		t.Errorf("task status = %q, want completed", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

// --- handleRecoveringInspectError tests ---

func TestHandleRecoveringInspectError_InstanceGone(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_rgone",
		Status:      models.TaskStatusRecovering,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "recovering task",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1
	o.inspectFailures["bf_rgone"] = 2 // should be cleared

	task, _ := s.GetTask(context.Background(), "bf_rgone")
	o.handleRecoveringInspectError(context.Background(), task, fmt.Errorf("InvalidInstanceId: i-abc"))

	task, _ = s.GetTask(context.Background(), "bf_rgone")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending (requeued)", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", task.RetryCount)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0 (wasRunning should decrement)", o.running)
	}
	if _, exists := o.inspectFailures["bf_rgone"]; exists {
		t.Error("inspectFailures should be cleared on instance gone")
	}
}

func TestHandleRecoveringInspectError_AccumulatesFailures(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_raccum",
		Status:      models.TaskStatusRecovering,
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_raccum")

	// First two failures — should accumulate without requeueing
	o.handleRecoveringInspectError(context.Background(), task, fmt.Errorf("connection refused"))
	o.handleRecoveringInspectError(context.Background(), task, fmt.Errorf("connection refused"))

	if o.inspectFailures["bf_raccum"] != 2 {
		t.Errorf("inspectFailures = %d, want 2", o.inspectFailures["bf_raccum"])
	}
	// Task should still be recovering in the store
	task, _ = s.GetTask(context.Background(), "bf_raccum")
	if task.Status != models.TaskStatusRecovering {
		t.Errorf("status = %q, want recovering (under threshold)", task.Status)
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1 (not yet requeued)", o.running)
	}
}

func TestHandleRecoveringInspectError_RequeuesAtMaxFailures(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_rmaxfail",
		Status:      models.TaskStatusRecovering,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "failing recovery",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1
	o.inspectFailures["bf_rmaxfail"] = maxInspectFailures - 1

	task, _ := s.GetTask(context.Background(), "bf_rmaxfail")
	o.handleRecoveringInspectError(context.Background(), task, fmt.Errorf("connection refused"))

	task, _ = s.GetTask(context.Background(), "bf_rmaxfail")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending (requeued after max failures)", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0 (wasRunning should decrement)", o.running)
	}
	if _, exists := o.inspectFailures["bf_rmaxfail"]; exists {
		t.Error("inspectFailures should be cleared after requeue")
	}
}

func TestRequeueRecoveringTask_WasRunning(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	now := time.Now().UTC()
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_rq",
		Status:      models.TaskStatusRecovering,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "re-queue me",
		InstanceID:  "local",
		ContainerID: "cont_rq",
		StartedAt:   &now,
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task, _ := s.GetTask(context.Background(), "bf_rq")
	o.requeueRecoveringTask(context.Background(), task, "instance gone", true)

	task, _ = s.GetTask(context.Background(), "bf_rq")
	if task.Status != models.TaskStatusPending {
		t.Errorf("task status = %q, want pending", task.Status)
	}
	if task.InstanceID != "" {
		t.Errorf("instance ID = %q, want empty", task.InstanceID)
	}
	if task.ContainerID != "" {
		t.Errorf("container ID = %q, want empty", task.ContainerID)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", task.RetryCount)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

func TestRequeueRecoveringTask_WasProvisioning(t *testing.T) {
	s := newMockStore()
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_prov_rq",
		Status:  models.TaskStatusRecovering,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "re-queue me",
	})

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 0

	task, _ := s.GetTask(context.Background(), "bf_prov_rq")
	o.requeueRecoveringTask(context.Background(), task, "no container", false)

	task, _ = s.GetTask(context.Background(), "bf_prov_rq")
	if task.Status != models.TaskStatusPending {
		t.Errorf("task status = %q, want pending", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry count = %d, want 1", task.RetryCount)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

// --- Error resilience tests (P0 #7) ---

func TestRecoverOnStartup_UpdateStatusError(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_recov_err",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "orphaned task",
		InstanceID:  "local",
		ContainerID: "cont1",
		StartedAt:   &now,
	})

	s.updateTaskStatusErr = fmt.Errorf("db connection lost")

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)

	// Should not panic despite UpdateTaskStatus failing
	o.recoverOnStartup(context.Background())
	bus.Close()

	// o.running should still be set correctly (independent of UpdateTaskStatus)
	if o.running != 1 {
		t.Errorf("running = %d, want 1 (should be set regardless of UpdateTaskStatus error)", o.running)
	}

	// Event should still be emitted
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRecovering {
		t.Errorf("expected [task.recovering], got %v", types)
	}
}

func TestRecoverOnStartup_ClearAssignmentError(t *testing.T) {
	s := newMockStore()

	s.CreateTask(context.Background(), &models.Task{
		ID:         "bf_clear_err",
		Status:     models.TaskStatusProvisioning,
		RepoURL:    "https://github.com/test/repo",
		Prompt:     "orphaned provisioning",
		InstanceID: "i-12345",
	})

	s.clearTaskAssignmentErr = fmt.Errorf("db connection lost")

	bus, n := newTestBus()
	o := newTestOrchestrator(s, bus)

	// Should not panic despite ClearTaskAssignment failing
	o.recoverOnStartup(context.Background())
	bus.Close()

	// Event should still be emitted
	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRecovering {
		t.Errorf("expected [task.recovering], got %v", types)
	}
}
