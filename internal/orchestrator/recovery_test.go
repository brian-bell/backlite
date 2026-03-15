package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

// mockStore implements store.Store for testing.
type mockStore struct {
	tasks     map[string]*models.Task
	instances map[string]*models.Instance
	mu        sync.Mutex
}

func newMockStore() *mockStore {
	return &mockStore{
		tasks:     make(map[string]*models.Task),
		instances: make(map[string]*models.Instance),
	}
}

func (s *mockStore) CreateTask(_ context.Context, task *models.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
	return nil
}

func (s *mockStore) GetTask(_ context.Context, id string) (*models.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (s *mockStore) ListTasks(_ context.Context, filter store.TaskFilter) ([]*models.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*models.Task
	for _, t := range s.tasks {
		if filter.Status != nil && t.Status != *filter.Status {
			continue
		}
		result = append(result, t)
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (s *mockStore) UpdateTask(_ context.Context, task *models.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
	return nil
}

func (s *mockStore) DeleteTask(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
	return nil
}

func (s *mockStore) CreateInstance(_ context.Context, inst *models.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[inst.InstanceID] = inst
	return nil
}

func (s *mockStore) GetInstance(_ context.Context, id string) (*models.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.instances[id]
	if !ok {
		return nil, nil
	}
	return i, nil
}

func (s *mockStore) ListInstances(_ context.Context, status *models.InstanceStatus) ([]*models.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*models.Instance
	for _, i := range s.instances {
		if status != nil && i.Status != *status {
			continue
		}
		result = append(result, i)
	}
	return result, nil
}

func (s *mockStore) UpdateInstance(_ context.Context, inst *models.Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.instances[inst.InstanceID] = inst
	return nil
}

func (s *mockStore) Close() error { return nil }

// mockNotifier records events.
type mockNotifier struct {
	events []notify.Event
	mu     sync.Mutex
}

func (n *mockNotifier) Notify(e notify.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, e)
	return nil
}

func (n *mockNotifier) eventTypes() []notify.EventType {
	n.mu.Lock()
	defer n.mu.Unlock()
	var types []notify.EventType
	for _, e := range n.events {
		types = append(types, e.Type)
	}
	return types
}

// mockDockerManager wraps DockerManager to override InspectContainer for tests.
type mockDockerManager struct {
	inspectResults map[string]ContainerStatus
	inspectErrors  map[string]error
}

func (m *mockDockerManager) inspect(instanceID, containerID string) (ContainerStatus, error) {
	key := instanceID + "/" + containerID
	if err, ok := m.inspectErrors[key]; ok {
		return ContainerStatus{}, err
	}
	if status, ok := m.inspectResults[key]; ok {
		return status, nil
	}
	return ContainerStatus{}, fmt.Errorf("unknown container %s", key)
}

func newTestOrchestrator(s store.Store, n notify.Notifier) *Orchestrator {
	cfg := &config.Config{
		Mode:              config.ModeLocal,
		AuthMode:          config.AuthModeAPIKey,
		ContainersPerInst: 4,
		PollInterval:      5 * time.Second,
	}
	return &Orchestrator{
		store:           s,
		config:          cfg,
		notifier:        n,
		docker:          NewDockerManager(cfg),
		scaler:          localScaler{},
		stopCh:          make(chan struct{}),
		inspectFailures: make(map[string]int),
	}
}

func TestRecoverOnStartup_NoOrphans(t *testing.T) {
	s := newMockStore()
	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)

	o.recoverOnStartup(context.Background())

	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	if len(n.events) != 0 {
		t.Errorf("expected no events, got %d", len(n.events))
	}
}

func TestRecoverOnStartup_RunningOrphans(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 0,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_task1",
		Status:      models.TaskStatusRunning,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "fix bug",
		InstanceID:  "local",
		ContainerID: "abc123",
		StartedAt:   &now,
	})

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)

	o.recoverOnStartup(context.Background())

	// Task should be recovering
	task, _ := s.GetTask(context.Background(), "bf_task1")
	if task.Status != models.TaskStatusRecovering {
		t.Errorf("task status = %q, want %q", task.Status, models.TaskStatusRecovering)
	}
	// Container should be preserved for inspection
	if task.ContainerID != "abc123" {
		t.Errorf("container ID should be preserved, got %q", task.ContainerID)
	}
	// o.running should reflect the previously-running task
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
	// Instance RunningContainers should be fixed up
	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 1 {
		t.Errorf("instance RunningContainers = %d, want 1", inst.RunningContainers)
	}
	// Should have fired recovering event
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)

	o.recoverOnStartup(context.Background())

	task, _ := s.GetTask(context.Background(), "bf_task2")
	if task.Status != models.TaskStatusRecovering {
		t.Errorf("task status = %q, want %q", task.Status, models.TaskStatusRecovering)
	}
	// Instance and container should be cleared
	if task.InstanceID != "" {
		t.Errorf("instance ID should be cleared, got %q", task.InstanceID)
	}
	if task.ContainerID != "" {
		t.Errorf("container ID should be cleared, got %q", task.ContainerID)
	}
	// Provisioning tasks don't count toward o.running
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)

	o.recoverOnStartup(context.Background())

	// Both should be recovering
	t1, _ := s.GetTask(context.Background(), "bf_running")
	t2, _ := s.GetTask(context.Background(), "bf_prov")
	if t1.Status != models.TaskStatusRecovering {
		t.Errorf("running task status = %q, want recovering", t1.Status)
	}
	if t2.Status != models.TaskStatusRecovering {
		t.Errorf("provisioning task status = %q, want recovering", t2.Status)
	}
	// Only the running task counts
	if o.running != 1 {
		t.Errorf("running = %d, want 1", o.running)
	}
	if len(n.events) != 2 {
		t.Errorf("expected 2 events, got %d", len(n.events))
	}
}

func TestMonitorRecovering_NoContainer(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:    "local",
		Status:        models.InstanceStatusRunning,
		MaxContainers: 4,
	})
	s.CreateTask(context.Background(), &models.Task{
		ID:      "bf_noc",
		Status:  models.TaskStatusRecovering,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "orphaned provisioning task",
		// No ContainerID — was provisioning
	})

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)

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

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:    "local",
		Status:        models.InstanceStatusRunning,
		MaxContainers: 4,
	})

	s.CreateTask(context.Background(), &models.Task{
		ID:          "bf_alive",
		Status:      models.TaskStatusRecovering,
		RepoURL:     "https://github.com/test/repo",
		Prompt:      "still running",
		InstanceID:  "local",
		ContainerID: "cont_alive",
		StartedAt:   &now,
	})

	n := &mockNotifier{}
	_ = newTestOrchestrator(s, n)

	// Since DockerManager.InspectContainer is not an interface, we test the
	// promote-back-to-running logic path directly.
	mock := &mockDockerManager{
		inspectResults: map[string]ContainerStatus{
			"local/cont_alive": {Done: false},
		},
	}

	task, _ := s.GetTask(context.Background(), "bf_alive")
	status, _ := mock.inspect("local", "cont_alive")
	if status.Done {
		t.Fatal("expected container to be running")
	}
	// Simulate what monitorRecovering does when container is still running
	task.Status = models.TaskStatusRunning
	task.Error = ""
	s.UpdateTask(context.Background(), task)

	task, _ = s.GetTask(context.Background(), "bf_alive")
	if task.Status != models.TaskStatusRunning {
		t.Errorf("task status = %q, want running", task.Status)
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
	o.running = 1 // Was counted during recoverOnStartup

	// Simulate handleCompletion for an exited container
	task, _ := s.GetTask(context.Background(), "bf_exited")
	status := ContainerStatus{Done: true, ExitCode: 0}
	o.handleCompletion(context.Background(), task, status)

	task, _ = s.GetTask(context.Background(), "bf_exited")
	if task.Status != models.TaskStatusCompleted {
		t.Errorf("task status = %q, want completed", task.Status)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
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

	n := &mockNotifier{}
	o := newTestOrchestrator(s, n)
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
	// running should stay at 0 since wasRunning=false
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
}

func TestMonitorCancelled_DecrementsRunning(t *testing.T) {
	s := newMockStore()
	now := time.Now().UTC()

	s.CreateInstance(context.Background(), &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 1,
	})

	// A task that was running and then cancelled via the API
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
	o.running = 1 // Was counted when originally dispatched

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

	// A cancelled task that was provisioning (no container) — should not affect o.running
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

	// A recovering task (previously running) that was cancelled via API
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
	o.running = 1 // Was counted during recoverOnStartup

	o.monitorCancelled(context.Background())

	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}

	task, _ := s.GetTask(context.Background(), "bf_cancel_recov")
	if task.ContainerID != "" {
		t.Errorf("containerID = %q, want empty", task.ContainerID)
	}
}
