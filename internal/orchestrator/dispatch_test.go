package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
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

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)

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

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)

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

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)

	_, err := o.findAvailableInstance(context.Background())
	if err != errNoCapacity {
		t.Errorf("expected errNoCapacity for terminated instance, got %v", err)
	}
}

func TestFindAvailableInstance_EmptyStore(t *testing.T) {
	s := newMockStore()
	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)

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

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
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

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)
	o.running = 1

	task := &models.Task{InstanceID: "local"}
	o.releaseSlot(context.Background(), task)

	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0 (should not go negative)", inst.RunningContainers)
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
	o := newTestOrchestrator(s, bus) // MaxConcurrent = ContainersPerInst = 4
	o.running = 4                    // at capacity

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

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)

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

func TestDispatch_FindInstanceDBError(t *testing.T) {
	s := newMockStore()
	s.listInstancesErr = fmt.Errorf("db connection pool exhausted")

	task := &models.Task{
		ID:      "bf_dberr",
		Status:  models.TaskStatusPending,
		RepoURL: "https://github.com/test/repo",
		Prompt:  "should fail with DB error",
	}
	s.CreateTask(context.Background(), task)

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(s, bus)

	task, _ = s.GetTask(context.Background(), "bf_dberr")
	err := o.dispatch(context.Background(), task)
	// A real DB error should propagate, not be silently treated as no-capacity
	if err == nil {
		t.Fatal("expected error from dispatch when ListInstances fails, got nil")
	}
}

func TestDispatch_ReadTaskWithoutEmbedder_Fails(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
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
func TestDispatch_ReadTask_OrchestratorMissingReaderImage_Fails(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
	task := &models.Task{
		ID:         "bf_read_no_reader",
		Status:     models.TaskStatusPending,
		TaskMode:   models.TaskModeRead,
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
// dispatch time: no instance reserved, no container launched, no embedding
// call, and a task.failed event with a duplicate-URL message is emitted.
func TestDispatch_ReadDuplicate_NonForce_FailsWithoutContainer(t *testing.T) {
	s := newMockStore()
	s.CreateInstance(context.Background(), newLocalInstance())
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
	if got.InstanceID != "" {
		t.Errorf("InstanceID = %q, want empty (no instance should be reserved)", got.InstanceID)
	}
	if o.running != 0 {
		t.Errorf("running = %d, want 0", o.running)
	}
	inst, _ := s.GetInstance(context.Background(), "local")
	if inst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0", inst.RunningContainers)
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
	s.CreateInstance(context.Background(), newLocalInstance())
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
	s.CreateInstance(context.Background(), newLocalInstance())
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
