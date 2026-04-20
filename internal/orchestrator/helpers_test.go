package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/embeddings"
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

	// Error injection: if set, CompleteTask returns this error.
	completeTaskErr error

	// Error injection: if set, the corresponding mock method returns this error.
	updateTaskStatusErr    error
	clearTaskAssignmentErr error
	markReadyForRetryErr   error
	decrementContainersErr error
	listInstancesErr       error
	upsertReadingErr       error
	getReadingByURLErr     error

	// Recorded reading calls.
	upsertedReadings []models.Reading

	// Pre-seeded readings for GetReadingByURL lookups, keyed by URL.
	readingsByURL map[string]*models.Reading
}

func newMockStore() *mockStore {
	return &mockStore{
		tasks:         make(map[string]*models.Task),
		instances:     make(map[string]*models.Instance),
		readingsByURL: make(map[string]*models.Reading),
	}
}

func (s *mockStore) CreateTask(_ context.Context, task *models.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *task
	s.tasks[task.ID] = &cp
	return nil
}

func (s *mockStore) HasAPIKeys(context.Context) (bool, error) { return false, nil }
func (s *mockStore) GetAPIKeyByHash(context.Context, string) (*models.APIKey, error) {
	return nil, store.ErrNotFound
}
func (s *mockStore) CreateAPIKey(context.Context, *models.APIKey) error { return nil }

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
	if s.listInstancesErr != nil {
		return nil, s.listInstancesErr
	}
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
	if s.updateTaskStatusErr != nil {
		return s.updateTaskStatusErr
	}
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
	if s.completeTaskErr != nil {
		return s.completeTaskErr
	}
	if t, ok := s.tasks[id]; ok {
		t.Status = result.Status
		t.Error = result.Error
		t.PRURL = result.PRURL
		t.OutputURL = result.OutputURL
		t.CostUSD = result.CostUSD
		t.ElapsedTimeSec = result.ElapsedTimeSec
		if result.RepoURL != "" {
			t.RepoURL = result.RepoURL
		}
		if result.TargetBranch != "" {
			t.TargetBranch = result.TargetBranch
		}
		if result.TaskMode != "" {
			t.TaskMode = result.TaskMode
		}
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
	if s.clearTaskAssignmentErr != nil {
		return s.clearTaskAssignmentErr
	}
	if t, ok := s.tasks[id]; ok {
		t.InstanceID = ""
		t.ContainerID = ""
	}
	return nil
}

func (s *mockStore) MarkReadyForRetry(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markReadyForRetryErr != nil {
		return s.markReadyForRetryErr
	}
	if t, ok := s.tasks[id]; ok {
		t.ReadyForRetry = true
	}
	return nil
}

func (s *mockStore) RetryTask(_ context.Context, id string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tasks[id]; ok {
		t.Status = models.TaskStatusPending
		t.InstanceID = ""
		t.ContainerID = ""
		t.ReadyForRetry = false
		t.RetryCount++
		t.UserRetryCount++
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
	if s.decrementContainersErr != nil {
		return s.decrementContainersErr
	}
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

func (s *mockStore) WithTx(_ context.Context, fn func(store.Store) error) error {
	return fn(s)
}

func (s *mockStore) UpsertReading(_ context.Context, r *models.Reading) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.upsertReadingErr != nil {
		return s.upsertReadingErr
	}
	cp := *r
	s.upsertedReadings = append(s.upsertedReadings, cp)
	stored := cp
	s.readingsByURL[r.URL] = &stored
	return nil
}

func (s *mockStore) GetReadingByURL(_ context.Context, url string) (*models.Reading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getReadingByURLErr != nil {
		return nil, s.getReadingByURLErr
	}
	r, ok := s.readingsByURL[url]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *r
	return &cp, nil
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

	// Error injection for StopContainer.
	stopContainerErr error

	// GetAgentOutput behavior.
	agentOutput    string
	agentOutputErr error
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
	return m.stopContainerErr
}

func (m *mockDockerManager) GetLogs(_ context.Context, _, _ string, _ int) (string, error) {
	return "", nil
}

func (m *mockDockerManager) GetAgentOutput(_ context.Context, _, _ string) (string, error) {
	if m.agentOutputErr != nil {
		return "", m.agentOutputErr
	}
	return m.agentOutput, nil
}

// --- Mock filesystem writer ---

type mockWriter struct {
	logSaves      []mockWriterLogSave
	metadataSaves []mockWriterMetadataSave
	err           error
}

type mockWriterLogSave struct {
	taskID string
	log    []byte
}

type mockWriterMetadataSave struct {
	taskID   string
	metadata any
}

func (m *mockWriter) SaveLog(_ context.Context, taskID string, logBytes []byte) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	m.logSaves = append(m.logSaves, mockWriterLogSave{taskID: taskID, log: logBytes})
	return "/api/v1/tasks/" + taskID + "/output", nil
}

func (m *mockWriter) SaveMetadata(_ context.Context, taskID string, metadata any) error {
	if m.err != nil {
		return m.err
	}
	m.metadataSaves = append(m.metadataSaves, mockWriterMetadataSave{taskID: taskID, metadata: metadata})
	return nil
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
		ContainersPerInst: 4,
		MaxUserRetries:    2,
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

func withOutputs(w Writer) func(*Orchestrator) {
	return func(o *Orchestrator) { o.outputs = w }
}

func withEmbedder(e embeddings.Embedder) func(*Orchestrator) {
	return func(o *Orchestrator) { o.embedder = e }
}

// mockEmbedder records Embed calls and returns a fixed vector or injected error.
type mockEmbedder struct {
	mu      sync.Mutex
	calls   []string
	vector  []float32
	errToFn error
}

func (m *mockEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, text)
	if m.errToFn != nil {
		return nil, m.errToFn
	}
	return m.vector, nil
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
