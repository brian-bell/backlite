package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

// --- Mock store ---

type mockStore struct {
	tasks     map[string]*models.Task
	instances map[string]*models.Instance
	mu        sync.Mutex

	// Error injection: if set, GetInstance returns this error instead of
	// looking up the instance map.
	getInstanceErr error
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
	cp := *task
	s.tasks[task.ID] = &cp
	return nil
}

func (s *mockStore) GetTask(_ context.Context, id string) (*models.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *mockStore) ListTasks(_ context.Context, filter store.TaskFilter) ([]*models.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*models.Task
	for _, t := range s.tasks {
		if filter.Status != nil && t.Status != *filter.Status {
			continue
		}
		cp := *t
		result = append(result, &cp)
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
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
	cp := *inst
	s.instances[inst.InstanceID] = &cp
	return nil
}

func (s *mockStore) GetInstance(_ context.Context, id string) (*models.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getInstanceErr != nil {
		return nil, s.getInstanceErr
	}
	i, ok := s.instances[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *i
	return &cp, nil
}

func (s *mockStore) ListInstances(_ context.Context, status *models.InstanceStatus) ([]*models.Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*models.Instance
	for _, i := range s.instances {
		if status != nil && i.Status != *status {
			continue
		}
		cp := *i
		result = append(result, &cp)
	}
	return result, nil
}

func (s *mockStore) UpdateTaskStatus(_ context.Context, id string, status models.TaskStatus, taskErr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.Status = status
		t.Error = taskErr
	}
	return nil
}

func (s *mockStore) AssignTask(_ context.Context, id string, instanceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.Status = models.TaskStatusProvisioning
		t.InstanceID = instanceID
	}
	return nil
}

func (s *mockStore) StartTask(_ context.Context, id string, containerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.Status = models.TaskStatusRunning
		t.ContainerID = containerID
		now := time.Now().UTC()
		t.StartedAt = &now
	}
	return nil
}

func (s *mockStore) CompleteTask(_ context.Context, id string, result store.TaskResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.Status = result.Status
		t.Error = result.Error
		t.PRURL = result.PRURL
		t.OutputURL = result.OutputURL
		t.CostUSD = result.CostUSD
		t.ElapsedTimeSec = result.ElapsedTimeSec
		now := time.Now().UTC()
		t.CompletedAt = &now
	}
	return nil
}

func (s *mockStore) RequeueTask(_ context.Context, id string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.Status = models.TaskStatusPending
		t.InstanceID = ""
		t.ContainerID = ""
		t.StartedAt = nil
		t.RetryCount++
		t.Error = "re-queued: " + reason
	}
	return nil
}

func (s *mockStore) CancelTask(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.Status = models.TaskStatusCancelled
		now := time.Now().UTC()
		t.CompletedAt = &now
	}
	return nil
}

func (s *mockStore) ClearTaskAssignment(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.InstanceID = ""
		t.ContainerID = ""
	}
	return nil
}

func (s *mockStore) UpdateInstanceStatus(_ context.Context, id string, status models.InstanceStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.instances[id]; ok {
		i.Status = status
		if status == models.InstanceStatusTerminated {
			i.RunningContainers = 0
		}
	}
	return nil
}

func (s *mockStore) IncrementRunningContainers(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.instances[id]; ok {
		i.RunningContainers++
	}
	return nil
}

func (s *mockStore) DecrementRunningContainers(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.instances[id]; ok {
		i.RunningContainers--
		if i.RunningContainers < 0 {
			i.RunningContainers = 0
		}
	}
	return nil
}

func (s *mockStore) UpdateInstanceDetails(_ context.Context, id string, privateIP, az string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.instances[id]; ok {
		i.PrivateIP = privateIP
		i.AvailabilityZone = az
	}
	return nil
}

func (s *mockStore) ResetRunningContainers(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i, ok := s.instances[id]; ok {
		i.RunningContainers = 0
	}
	return nil
}

func (s *mockStore) GetAllowedSender(_ context.Context, channelType, address string) (*models.AllowedSender, error) {
	return nil, store.ErrNotFound
}

func (s *mockStore) CreateAllowedSender(_ context.Context, sender *models.AllowedSender) error {
	return nil
}

func (s *mockStore) UpsertDiscordInstall(_ context.Context, _ *models.DiscordInstall) error {
	return nil
}

func (s *mockStore) GetDiscordInstall(_ context.Context, _ string) (*models.DiscordInstall, error) {
	return nil, store.ErrNotFound
}

func (s *mockStore) DeleteDiscordInstall(_ context.Context, _ string) error {
	return nil
}

func (s *mockStore) WithTx(_ context.Context, fn func(store.Store) error) error {
	return fn(s)
}

func (s *mockStore) Close() error { return nil }

// --- Mock notifier ---

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

// --- Mock docker manager (implements Runner) ---

type mockDockerManager struct {
	inspectResults map[string]ContainerStatus
	inspectErrors  map[string]error

	// RunAgent behavior: if runAgentFn is set it takes priority; otherwise
	// returns runAgentID/runAgentErr.
	runAgentFn  func(ctx context.Context, instance *models.Instance, task *models.Task) (string, error)
	runAgentID  string
	runAgentErr error
}

func (m *mockDockerManager) RunAgent(ctx context.Context, instance *models.Instance, task *models.Task) (string, error) {
	if m.runAgentFn != nil {
		return m.runAgentFn(ctx, instance, task)
	}
	if m.runAgentErr != nil {
		return "", m.runAgentErr
	}
	if m.runAgentID != "" {
		return m.runAgentID, nil
	}
	return "", fmt.Errorf("not implemented")
}

func (m *mockDockerManager) InspectContainer(_ context.Context, instanceID, containerID string) (ContainerStatus, error) {
	key := instanceID + "/" + containerID
	if err, ok := m.inspectErrors[key]; ok {
		return ContainerStatus{}, err
	}
	if status, ok := m.inspectResults[key]; ok {
		return status, nil
	}
	return ContainerStatus{}, fmt.Errorf("unknown container %s", key)
}

func (m *mockDockerManager) StopContainer(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockDockerManager) GetLogs(_ context.Context, _, _ string, _ int) (string, error) {
	return "", nil
}

func (m *mockDockerManager) GetAgentOutput(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

// --- Mock S3 client ---

type mockS3Client struct {
	uploads []mockS3Upload
	err     error
}

type mockS3Upload struct {
	key  string
	data []byte
}

func (m *mockS3Client) Upload(_ context.Context, key string, data []byte) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.uploads = append(m.uploads, mockS3Upload{key: key, data: data})
	return fmt.Sprintf("s3://test-bucket/%s", key), nil
}

func (m *mockS3Client) UploadJSON(_ context.Context, key string, data []byte) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.uploads = append(m.uploads, mockS3Upload{key: key, data: data})
	return fmt.Sprintf("s3://test-bucket/%s", key), nil
}

func (m *mockS3Client) PresignGetURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return fmt.Sprintf("https://test-bucket.s3.amazonaws.com/%s?presigned", key), nil
}

// newTestBus creates an EventBus with a mockNotifier subscribed.
// Call bus.Close() before reading events from the notifier.
func newTestBus() (*notify.EventBus, *mockNotifier) {
	bus := notify.NewEventBus()
	n := &mockNotifier{}
	bus.Subscribe(n)
	return bus, n
}

// --- Test orchestrator constructor ---

func newTestOrchestrator(s store.Store, bus *notify.EventBus, opts ...func(*Orchestrator)) *Orchestrator {
	cfg := &config.Config{
		Mode:              config.ModeLocal,
		AuthMode:          config.AuthModeAPIKey,
		ContainersPerInst: 4,
		PollInterval:      5 * time.Second,
	}
	o := &Orchestrator{
		store:           s,
		config:          cfg,
		bus:             bus,
		docker:          &mockDockerManager{},
		scaler:          NoopScaler{},
		stopCh:          make(chan struct{}),
		inspectFailures: make(map[string]int),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func withDocker(d Runner) func(*Orchestrator) {
	return func(o *Orchestrator) { o.docker = d }
}

func withS3(s S3Client) func(*Orchestrator) {
	return func(o *Orchestrator) { o.s3 = s }
}

// newLocalInstance creates a standard local instance for tests.
func newLocalInstance() *models.Instance {
	return &models.Instance{
		InstanceID:        "local",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 0,
	}
}
