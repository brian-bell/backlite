package orchestrator

import (
	"context"
	"fmt"
	"testing"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
)

func TestFindAvailableInstance_ReturnsInstanceWithCapacity(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "i-full",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     2,
		RunningContainers: 2,
	})
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "i-avail",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	o := newTestOrchestrator(s, &mockNotifier{})

	inst, err := o.findAvailableInstance(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.InstanceID != "i-avail" {
		t.Errorf("instance = %q, want i-avail", inst.InstanceID)
	}
}

func TestFindAvailableInstance_NoCapacity(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "i-full",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     2,
		RunningContainers: 2,
	})

	o := newTestOrchestrator(s, &mockNotifier{})

	_, err := o.findAvailableInstance(context.Background())
	if err != errNoCapacity {
		t.Errorf("expected errNoCapacity, got %v", err)
	}
}

func TestFindAvailableInstance_IgnoresNonRunning(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "i-terminated",
		Status:            models.InstanceStatusTerminated,
		MaxContainers:     4,
		RunningContainers: 0,
	})

	o := newTestOrchestrator(s, &mockNotifier{})

	_, err := o.findAvailableInstance(context.Background())
	if err != errNoCapacity {
		t.Errorf("expected errNoCapacity for terminated instance, got %v", err)
	}
}

func TestFindAvailableInstance_EmptyStore(t *testing.T) {
	s := newMockStore()
	o := newTestOrchestrator(s, &mockNotifier{})

	_, err := o.findAvailableInstance(context.Background())
	if err != errNoCapacity {
		t.Errorf("expected errNoCapacity for empty store, got %v", err)
	}
}

func TestReleaseSlot(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 2,
	})

	o := newTestOrchestrator(s, &mockNotifier{})
	o.running = 2

	task := &models.Task{InstanceID: "local"}
	o.releaseSlot(context.Background(), task)

	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}

	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 1 {
		t.Errorf("RunningContainers = %d, want 1", inst.RunningContainers)
	}
}

func TestReleaseSlot_PreventsNegativeContainers(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 0,
	})

	o := newTestOrchestrator(s, &mockNotifier{})
	o.running = 1

	task := &models.Task{InstanceID: "local"}
	o.releaseSlot(context.Background(), task)

	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0 (should not go negative)", inst.RunningContainers)
	}
}

func TestMarkInstanceTerminated(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "i-abc",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 2,
	})

	o := newTestOrchestrator(s, &mockNotifier{})

	o.markInstanceTerminated(context.Background(), "i-abc")

	inst, _ := s.GetInstance(context.Background(), "i-abc")
	if inst.Status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", inst.Status)
	}
	if inst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0", inst.RunningContainers)
	}
}

func TestMarkInstanceTerminated_AlreadyTerminated(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "i-abc",
		Status:            models.InstanceStatusTerminated,
		MaxContainers:     4,
		RunningContainers: 0,
	})

	o := newTestOrchestrator(s, &mockNotifier{})

	// Should be a no-op, not panic
	o.markInstanceTerminated(context.Background(), "i-abc")

	inst, _ := s.GetInstance(context.Background(), "i-abc")
	if inst.Status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", inst.Status)
	}
}

func TestMarkInstanceTerminated_EmptyID(t *testing.T) {
	o := newTestOrchestrator(newMockStore(), &mockNotifier{})
	// Should not panic
	o.markInstanceTerminated(context.Background(), "")
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n) // MaxConcurrent = ContainersPerInst = 4
	o.running = 4                  // at capacity

	o.dispatchPending(context.Background())

	task, _ := s.GetTask(context.Background(), "bf_blocked")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending (no capacity)", task.Status)
	}
}

func TestDispatchPending_DispatchesTask(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_disp",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "dispatch me",
	})

	n := &mockNotifier{}
	mock := &mockDockerManager{
		runAgentID:     "container-abc",
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))

	o.dispatchPending(context.Background())

	task, _ := s.GetTask(context.Background(), "bf_disp")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", task.Status)
	}
	if task.ContainerID != "container-abc" {
		t.Errorf("containerID = %q, want container-abc", task.ContainerID)
	}
	if task.InstanceID != "local" {
		t.Errorf("instanceID = %q, want local", task.InstanceID)
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

func TestDispatchPending_FailedDispatch(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_dfail",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "fail to dispatch",
	})

	n := &mockNotifier{}
	mock := &mockDockerManager{
		runAgentErr:    fmt.Errorf("docker daemon unavailable"),
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))

	o.dispatchPending(context.Background())

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

func TestDispatch_NoAvailableInstance(t *testing.T) {
	s := newMockStore()
	// No instances at all
	task := &models.Task{
		ID:      "bf_noinst",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "no instance",
	}
	s.CreateTask(context.Background(), task)

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)

	task, _ = s.GetTask(context.Background(), "bf_noinst")
	err := o.dispatch(context.Background(), task)
	if err != nil {
		t.Errorf("dispatch should return nil when no capacity (triggers scale-up), got %v", err)
	}

	// Task should still be pending (not modified by dispatch when no instance)
	task, _ = s.GetTask(context.Background(), "bf_noinst")
	if task.Status != models.TaskStatusPending {
		t.Errorf("status = %q, want pending", task.Status)
	}
}

func TestDispatch_Success(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
	task := &models.Task{
		ID:      "bf_dsuc",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "succeed",
	}
	s.CreateTask(context.Background(), task)

	n := &mockNotifier{}
	mock := &mockDockerManager{
		runAgentID:     "cont-xyz",
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))

	task, _ = s.GetTask(context.Background(), "bf_dsuc")
	err := o.dispatch(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task, _ = s.GetTask(context.Background(), "bf_dsuc")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("status = %q, want running", task.Status)
	}
	if task.ContainerID != "cont-xyz" {
		t.Errorf("containerID = %q, want cont-xyz", task.ContainerID)
	}
	if task.InstanceID != "local" {
		t.Errorf("instanceID = %q, want local", task.InstanceID)
	}
	if task.StartedAt == nil {
		t.Error("StartedAt should be set")
	}
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}

	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 1 {
		t.Errorf("RunningContainers = %d, want 1", inst.RunningContainers)
	}

	types := n.eventTypes()
	if len(types) != 1 || types[0] != notify.EventTaskRunning {
		t.Errorf("expected [task.running], got %v", types)
	}
}

func TestDispatch_RunAgentError(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
	task := &models.Task{
		ID:      "bf_derr",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "run agent fails",
	}
	s.CreateTask(context.Background(), task)

	n := &mockNotifier{}
	mock := &mockDockerManager{
		runAgentErr:    fmt.Errorf("image pull failed"),
		inspectResults: map[string]ContainerStatus{},
	}
	o := newTestOrchestrator(s, n, withDocker(mock))

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
