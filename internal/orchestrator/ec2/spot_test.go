package ec2

import (
	"context"
	"testing"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

type recordingEmitter struct {
	events []notify.Event
}

func (e *recordingEmitter) Emit(event notify.Event) {
	e.events = append(e.events, event)
}

type interruptionStore struct {
	task           *models.Task
	emitter        *recordingEmitter
	requeueChecked bool
}

func (s *interruptionStore) ListInstances(context.Context, *models.InstanceStatus) ([]*models.Instance, error) {
	return nil, nil
}

func (s *interruptionStore) UpdateInstanceStatus(context.Context, string, models.InstanceStatus) error {
	return nil
}

func (s *interruptionStore) ListTasks(_ context.Context, filter store.TaskFilter) ([]*models.Task, error) {
	if filter.Status != nil && *filter.Status == models.TaskStatusRunning {
		return []*models.Task{s.task}, nil
	}
	return nil, nil
}

func (s *interruptionStore) RequeueTask(_ context.Context, id string, reason string) error {
	s.requeueChecked = true
	if len(s.emitter.events) == 0 {
		return &testError{"event not emitted before requeue"}
	}
	if s.emitter.events[0].Type != notify.EventTaskInterrupted {
		return &testError{"wrong event emitted before requeue"}
	}
	if id != s.task.ID {
		return &testError{"unexpected task id"}
	}
	if reason != "spot interruption" {
		return &testError{"unexpected requeue reason"}
	}
	return nil
}

func (s *interruptionStore) UpdateTaskStatus(context.Context, string, models.TaskStatus, string) error {
	return nil
}

func (s *interruptionStore) CreateTask(context.Context, *models.Task) error { return nil }
func (s *interruptionStore) GetTask(context.Context, string) (*models.Task, error) {
	return nil, store.ErrNotFound
}
func (s *interruptionStore) HasAPIKeys(context.Context) (bool, error) { return false, nil }
func (s *interruptionStore) GetAPIKeyByHash(context.Context, string) (*models.APIKey, error) {
	return nil, store.ErrNotFound
}
func (s *interruptionStore) CreateAPIKey(context.Context, *models.APIKey) error { return nil }
func (s *interruptionStore) DeleteTask(context.Context, string) error           { return nil }
func (s *interruptionStore) AssignTask(context.Context, string, string) error   { return nil }
func (s *interruptionStore) StartTask(context.Context, string, string) error    { return nil }
func (s *interruptionStore) CompleteTask(context.Context, string, store.TaskResult) error {
	return nil
}
func (s *interruptionStore) CancelTask(context.Context, string) error               { return nil }
func (s *interruptionStore) ClearTaskAssignment(context.Context, string) error      { return nil }
func (s *interruptionStore) MarkReadyForRetry(context.Context, string) error        { return nil }
func (s *interruptionStore) RetryTask(context.Context, string, int) error           { return nil }
func (s *interruptionStore) CreateInstance(context.Context, *models.Instance) error { return nil }
func (s *interruptionStore) GetInstance(context.Context, string) (*models.Instance, error) {
	return nil, store.ErrNotFound
}
func (s *interruptionStore) UpdateInstanceDetails(context.Context, string, string, string) error {
	return nil
}
func (s *interruptionStore) IncrementRunningContainers(context.Context, string) error { return nil }
func (s *interruptionStore) DecrementRunningContainers(context.Context, string) error { return nil }
func (s *interruptionStore) ResetRunningContainers(context.Context, string) error     { return nil }
func (s *interruptionStore) GetAllowedSender(context.Context, string, string) (*models.AllowedSender, error) {
	return nil, store.ErrNotFound
}
func (s *interruptionStore) CreateAllowedSender(context.Context, *models.AllowedSender) error {
	return nil
}
func (s *interruptionStore) UpsertDiscordInstall(context.Context, *models.DiscordInstall) error {
	return nil
}
func (s *interruptionStore) GetDiscordInstall(context.Context, string) (*models.DiscordInstall, error) {
	return nil, store.ErrNotFound
}
func (s *interruptionStore) DeleteDiscordInstall(context.Context, string) error { return nil }
func (s *interruptionStore) UpsertDiscordTaskThread(context.Context, *models.DiscordTaskThread) error {
	return nil
}
func (s *interruptionStore) GetDiscordTaskThread(context.Context, string) (*models.DiscordTaskThread, error) {
	return nil, store.ErrNotFound
}
func (s *interruptionStore) CreateReading(context.Context, *models.Reading) error       { return nil }
func (s *interruptionStore) UpsertReading(context.Context, *models.Reading) error       { return nil }
func (s *interruptionStore) WithTx(_ context.Context, fn func(store.Store) error) error { return fn(s) }
func (s *interruptionStore) Close() error                                               { return nil }

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestHandleInterruption_EmitsBeforeRequeue(t *testing.T) {
	emitter := &recordingEmitter{}
	st := &interruptionStore{
		task: &models.Task{
			ID:         "bf_spot_1",
			Status:     models.TaskStatusRunning,
			InstanceID: "i-123",
		},
		emitter: emitter,
	}
	h := NewSpotHandler(st, nil, emitter)

	h.handleInterruption(context.Background(), &models.Instance{InstanceID: "i-123"})

	if len(emitter.events) != 1 {
		t.Fatalf("event count = %d, want 1", len(emitter.events))
	}
	if emitter.events[0].Type != notify.EventTaskInterrupted {
		t.Fatalf("event type = %s, want %s", emitter.events[0].Type, notify.EventTaskInterrupted)
	}
	if !st.requeueChecked {
		t.Fatal("requeue path was not executed")
	}
}
