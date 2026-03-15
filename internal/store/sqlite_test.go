package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/models"
)

func testStore(t *testing.T) *SQLiteStore {
	t.Helper()
	f, err := os.CreateTemp("", "backflow-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := NewSQLite(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTaskCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	task := &models.Task{
		ID:           "bf_TEST001",
		Status:       models.TaskStatusPending,
		TaskMode:     models.TaskModeCode,
		Harness:      models.HarnessClaudeCode,
		RepoURL:      "https://github.com/test/repo",
		Branch:       "backflow/test",
		TargetBranch: "main",
		Prompt:       "Fix the bug",
		Model:        "claude-sonnet-4-6",
		MaxBudgetUSD: 10.0,
		MaxTurns:     200,
		CreatePR:     true,
		PRTitle:      "Fix bug",
		AllowedTools: []string{"Read", "Write"},
		EnvVars:      map[string]string{"FOO": "bar"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// Create
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Get
	got, err := s.GetTask(ctx, "bf_TEST001")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetTask returned nil")
	}
	if got.Harness != models.HarnessClaudeCode {
		t.Errorf("Harness = %q, want %q", got.Harness, models.HarnessClaudeCode)
	}
	if got.RepoURL != task.RepoURL {
		t.Errorf("RepoURL = %q, want %q", got.RepoURL, task.RepoURL)
	}
	if got.Prompt != task.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, task.Prompt)
	}
	if got.TaskMode != models.TaskModeCode {
		t.Errorf("TaskMode = %q, want %q", got.TaskMode, models.TaskModeCode)
	}
	if !got.CreatePR {
		t.Error("CreatePR should be true")
	}
	if len(got.AllowedTools) != 2 {
		t.Errorf("AllowedTools len = %d, want 2", len(got.AllowedTools))
	}
	if got.EnvVars["FOO"] != "bar" {
		t.Errorf("EnvVars[FOO] = %q, want %q", got.EnvVars["FOO"], "bar")
	}

	// Update
	got.Status = models.TaskStatusRunning
	startedAt := now.Add(time.Minute)
	got.StartedAt = &startedAt
	if err := s.UpdateTask(ctx, got); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	got2, _ := s.GetTask(ctx, "bf_TEST001")
	if got2.Status != models.TaskStatusRunning {
		t.Errorf("Status = %q, want %q", got2.Status, models.TaskStatusRunning)
	}

	// List
	tasks, err := s.ListTasks(ctx, TaskFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("ListTasks len = %d, want 1", len(tasks))
	}

	// List with filter
	pending := models.TaskStatusPending
	tasks, _ = s.ListTasks(ctx, TaskFilter{Status: &pending})
	if len(tasks) != 0 {
		t.Errorf("ListTasks(pending) len = %d, want 0", len(tasks))
	}

	// Delete
	if err := s.DeleteTask(ctx, "bf_TEST001"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	got3, _ := s.GetTask(ctx, "bf_TEST001")
	if got3 != nil {
		t.Error("expected nil after delete")
	}
}

func TestGetTaskNotFound(t *testing.T) {
	s := testStore(t)
	got, err := s.GetTask(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent task")
	}
}

func TestInstanceCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	inst := &models.Instance{
		InstanceID:        "i-test123",
		InstanceType:      "m7g.xlarge",
		AvailabilityZone:  "us-east-1a",
		PrivateIP:         "10.0.1.5",
		Status:            models.InstanceStatusRunning,
		MaxContainers:     4,
		RunningContainers: 0,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	got, err := s.GetInstance(ctx, "i-test123")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if got.InstanceType != "m7g.xlarge" {
		t.Errorf("InstanceType = %q, want m7g.xlarge", got.InstanceType)
	}
	if got.MaxContainers != 4 {
		t.Errorf("MaxContainers = %d, want 4", got.MaxContainers)
	}

	// Update
	got.RunningContainers = 2
	if err := s.UpdateInstance(ctx, got); err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}

	// List
	running := models.InstanceStatusRunning
	instances, err := s.ListInstances(ctx, &running)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Errorf("ListInstances len = %d, want 1", len(instances))
	}
	if instances[0].RunningContainers != 2 {
		t.Errorf("RunningContainers = %d, want 2", instances[0].RunningContainers)
	}
}

func TestReviewTaskCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	task := &models.Task{
		ID:             "bf_REVIEW01",
		Status:         models.TaskStatusPending,
		TaskMode:       models.TaskModeReview,
		RepoURL:        "https://github.com/test/repo",
		ReviewPRNumber: 42,
		Prompt:         "Focus on security",
		Model:          "claude-sonnet-4-6",
		MaxBudgetUSD:   5.0,
		MaxTurns:       50,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask(ctx, "bf_REVIEW01")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetTask returned nil")
	}
	if got.TaskMode != models.TaskModeReview {
		t.Errorf("TaskMode = %q, want %q", got.TaskMode, models.TaskModeReview)
	}
	if got.ReviewPRNumber != 42 {
		t.Errorf("ReviewPRNumber = %d, want 42", got.ReviewPRNumber)
	}
	if got.Prompt != "Focus on security" {
		t.Errorf("Prompt = %q, want %q", got.Prompt, "Focus on security")
	}
}
