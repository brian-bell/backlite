package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

// taskActionStore implements the subset of store.Store needed by CancelTask and RetryTask.
type taskActionStore struct {
	store.Store
	task      *models.Task
	getErr    error
	cancelErr error
	retryErr  error
	cancelled []string
	retried   []string
}

func (s *taskActionStore) GetTask(_ context.Context, id string) (*models.Task, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.task != nil && s.task.ID == id {
		return s.task, nil
	}
	return nil, store.ErrNotFound
}

func (s *taskActionStore) CancelTask(_ context.Context, id string) error {
	s.cancelled = append(s.cancelled, id)
	return s.cancelErr
}

func (s *taskActionStore) RetryTask(_ context.Context, id string, _ int) error {
	s.retried = append(s.retried, id)
	return s.retryErr
}

// --- CancelTask tests ---

func TestCancelTask_RunningTask(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusRunning},
	}
	bus := &capturingEmitter{}

	err := CancelTask(context.Background(), "bf_1", s, bus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.cancelled) != 1 || s.cancelled[0] != "bf_1" {
		t.Errorf("cancelled = %v, want [bf_1]", s.cancelled)
	}
	if len(bus.events) != 1 || bus.events[0].Type != notify.EventTaskCancelled {
		t.Errorf("events = %v, want one EventTaskCancelled", bus.events)
	}
}

func TestCancelTask_ProvisioningTask(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusProvisioning},
	}
	bus := &capturingEmitter{}

	err := CancelTask(context.Background(), "bf_1", s, bus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.cancelled) != 1 {
		t.Errorf("cancelled = %v, want [bf_1]", s.cancelled)
	}
}

func TestCancelTask_PendingTask(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusPending},
	}
	bus := &capturingEmitter{}

	err := CancelTask(context.Background(), "bf_1", s, bus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.cancelled) != 1 {
		t.Errorf("cancelled = %v, want [bf_1]", s.cancelled)
	}
}

func TestCancelTask_RecoveringTask(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusRecovering},
	}
	bus := &capturingEmitter{}

	err := CancelTask(context.Background(), "bf_1", s, bus)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.cancelled) != 1 {
		t.Errorf("cancelled = %v, want [bf_1]", s.cancelled)
	}
}

func TestCancelTask_CompletedTask_ReturnsError(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusCompleted},
	}
	bus := &capturingEmitter{}

	err := CancelTask(context.Background(), "bf_1", s, bus)
	if err == nil {
		t.Fatal("expected error for completed task")
	}
	if len(s.cancelled) != 0 {
		t.Errorf("should not have called CancelTask on store")
	}
	if len(bus.events) != 0 {
		t.Errorf("should not have emitted events")
	}
}

func TestCancelTask_NotFound(t *testing.T) {
	s := &taskActionStore{getErr: store.ErrNotFound}
	bus := &capturingEmitter{}

	err := CancelTask(context.Background(), "bf_missing", s, bus)
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestCancelTask_StoreError(t *testing.T) {
	s := &taskActionStore{
		task:      &models.Task{ID: "bf_1", Status: models.TaskStatusRunning},
		cancelErr: fmt.Errorf("db error"),
	}
	bus := &capturingEmitter{}

	err := CancelTask(context.Background(), "bf_1", s, bus)
	if err == nil {
		t.Fatal("expected error when store fails")
	}
	if len(bus.events) != 0 {
		t.Errorf("should not emit event on store failure")
	}
}

// --- RetryTask tests ---

func TestRetryTask_FailedTask_Ready(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusFailed, ReadyForRetry: true},
	}

	err := RetryTask(context.Background(), "bf_1", s, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.retried) != 1 || s.retried[0] != "bf_1" {
		t.Errorf("retried = %v, want [bf_1]", s.retried)
	}
}

func TestRetryTask_InterruptedTask_Ready(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusInterrupted, ReadyForRetry: true},
	}

	err := RetryTask(context.Background(), "bf_1", s, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.retried) != 1 {
		t.Errorf("retried = %v, want [bf_1]", s.retried)
	}
}

func TestRetryTask_CancelledTask_Ready(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusCancelled, ReadyForRetry: true},
	}

	err := RetryTask(context.Background(), "bf_1", s, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.retried) != 1 {
		t.Errorf("retried = %v, want [bf_1]", s.retried)
	}
}

func TestRetryTask_NotReady_ReturnsError(t *testing.T) {
	s := &taskActionStore{
		task:     &models.Task{ID: "bf_1", Status: models.TaskStatusCancelled, ReadyForRetry: false},
		retryErr: fmt.Errorf("task bf_1 is not ready for retry"),
	}

	err := RetryTask(context.Background(), "bf_1", s, 2)
	if err == nil {
		t.Fatal("expected error when task is not ready for retry")
	}
	if len(s.retried) != 1 {
		t.Errorf("should have attempted atomic retry (pre-flight passes), got %v", s.retried)
	}
}

func TestRetryTask_CapReached_ReturnsError(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusFailed, ReadyForRetry: true, UserRetryCount: 2},
	}

	err := RetryTask(context.Background(), "bf_1", s, 2)
	if err == nil {
		t.Fatal("expected error when retry cap is reached")
	}
	if len(s.retried) != 0 {
		t.Errorf("should not have attempted retry, got %v", s.retried)
	}
}

func TestRetryTask_RunningTask_ReturnsError(t *testing.T) {
	s := &taskActionStore{
		task: &models.Task{ID: "bf_1", Status: models.TaskStatusRunning},
	}

	err := RetryTask(context.Background(), "bf_1", s, 2)
	if err == nil {
		t.Fatal("expected error for running task")
	}
	if len(s.retried) != 0 {
		t.Errorf("should not have retried")
	}
}

func TestRetryTask_NotFound(t *testing.T) {
	s := &taskActionStore{getErr: store.ErrNotFound}

	err := RetryTask(context.Background(), "bf_missing", s, 2)
	if err == nil {
		t.Fatal("expected error for missing task")
	}
}

func TestRetryTask_StoreError(t *testing.T) {
	s := &taskActionStore{
		task:     &models.Task{ID: "bf_1", Status: models.TaskStatusFailed, ReadyForRetry: true},
		retryErr: fmt.Errorf("db error"),
	}

	err := RetryTask(context.Background(), "bf_1", s, 2)
	if err == nil {
		t.Fatal("expected error when store fails")
	}
}
