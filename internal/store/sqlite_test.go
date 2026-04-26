package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/models"
)

// sqliteTestTask creates a minimal task and inserts it.
func sqliteTestTask(t *testing.T, s *SQLiteStore) *models.Task {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := &models.Task{
		ID:        "bf_TEST001",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Branch:    "backlite/test",
		Prompt:    "Fix the bug",
		Model:     "claude-sonnet-4-6",
		CreatePR:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func testSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	ctx := context.Background()
	migrationsDir := filepath.Join("..", "..", "migrations")
	dbPath := filepath.Join(t.TempDir(), sanitizeTestName(t.Name())+"-test.db")
	s, err := NewSQLite(ctx, dbPath, migrationsDir)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return s
}

func sanitizeTestName(name string) string {
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, " ", "-")
	return name
}

func TestSQLite_TaskRoundTrip(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &models.Task{
		ID:              "bf_TEST001",
		Status:          models.TaskStatusPending,
		TaskMode:        models.TaskModeCode,
		Harness:         models.HarnessClaudeCode,
		RepoURL:         "https://github.com/test/repo",
		Branch:          "backlite/test",
		TargetBranch:    "main",
		Prompt:          "Fix the bug",
		Model:           "claude-sonnet-4-6",
		AgentImage:      "backlite-agent:v2",
		MaxBudgetUSD:    10.0,
		MaxTurns:        200,
		CreatePR:        true,
		SaveAgentOutput: true,
		AllowedTools:    []string{"Read", "Write"},
		EnvVars:         map[string]string{"FOO": "bar"},
		CreatedAt:       now,
		UpdatedAt:       now,
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
	if got.ID != "bf_TEST001" {
		t.Errorf("ID = %q, want bf_TEST001", got.ID)
	}
	if got.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusPending)
	}
	if got.TaskMode != models.TaskModeCode {
		t.Errorf("TaskMode = %q, want %q", got.TaskMode, models.TaskModeCode)
	}
	if got.Harness != models.HarnessClaudeCode {
		t.Errorf("Harness = %q, want %q", got.Harness, models.HarnessClaudeCode)
	}
	if got.RepoURL != "https://github.com/test/repo" {
		t.Errorf("RepoURL = %q", got.RepoURL)
	}
	if got.Prompt != "Fix the bug" {
		t.Errorf("Prompt = %q", got.Prompt)
	}
	if !got.CreatePR {
		t.Error("CreatePR should be true")
	}
	if !got.SaveAgentOutput {
		t.Error("SaveAgentOutput should be true")
	}
	if got.MaxBudgetUSD != 10.0 {
		t.Errorf("MaxBudgetUSD = %f, want 10.0", got.MaxBudgetUSD)
	}
	if got.MaxTurns != 200 {
		t.Errorf("MaxTurns = %d, want 200", got.MaxTurns)
	}
	if len(got.AllowedTools) != 2 || got.AllowedTools[0] != "Read" || got.AllowedTools[1] != "Write" {
		t.Errorf("AllowedTools = %v, want [Read Write]", got.AllowedTools)
	}
	if got.EnvVars["FOO"] != "bar" {
		t.Errorf("EnvVars[FOO] = %q, want bar", got.EnvVars["FOO"])
	}
	if got.AgentImage != "backlite-agent:v2" {
		t.Errorf("AgentImage = %q, want %q", got.AgentImage, "backlite-agent:v2")
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
}

func TestSQLite_TaskRoundTrip_DefaultAgentImage(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &models.Task{
		ID:        "bf_TEST_NOIMG",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "Fix bug",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask(ctx, "bf_TEST_NOIMG")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.AgentImage != "" {
		t.Errorf("AgentImage = %q, want empty (default)", got.AgentImage)
	}
	if got.Force {
		t.Errorf("Force = true, want false (default)")
	}
}

func TestSQLite_CreateTask_PersistsForce(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &models.Task{
		ID:        "bf_TEST_FORCE",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeRead,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "https://example.com/post",
		Force:     true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask(ctx, "bf_TEST_FORCE")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !got.Force {
		t.Errorf("Force = false, want true")
	}
}

func TestSQLite_CreateTask_PersistsParentTaskID(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	parent := &models.Task{
		ID:        "bf_PARENT0001",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "Fix the bug",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, parent); err != nil {
		t.Fatalf("CreateTask parent: %v", err)
	}

	parentID := parent.ID
	child := &models.Task{
		ID:           "bf_CHILD00001",
		Status:       models.TaskStatusPending,
		TaskMode:     models.TaskModeReview,
		Harness:      models.HarnessClaudeCode,
		Prompt:       "Review the PR",
		ParentTaskID: &parentID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.CreateTask(ctx, child); err != nil {
		t.Fatalf("CreateTask child: %v", err)
	}

	got, err := s.GetTask(ctx, "bf_CHILD00001")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ParentTaskID == nil {
		t.Fatalf("ParentTaskID = nil, want %q", parentID)
	}
	if *got.ParentTaskID != parentID {
		t.Errorf("ParentTaskID = %q, want %q", *got.ParentTaskID, parentID)
	}

	// Sibling task without a parent has nil ParentTaskID.
	gotParent, err := s.GetTask(ctx, "bf_PARENT0001")
	if err != nil {
		t.Fatalf("GetTask parent: %v", err)
	}
	if gotParent.ParentTaskID != nil {
		t.Errorf("ParentTaskID = %v, want nil", gotParent.ParentTaskID)
	}
}

func TestSQLite_CreateTask_RejectsUnknownParentTaskID(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	missing := "bf_DOES_NOT_EXIST"
	child := &models.Task{
		ID:           "bf_ORPHAN0001",
		Status:       models.TaskStatusPending,
		TaskMode:     models.TaskModeCode,
		Harness:      models.HarnessClaudeCode,
		Prompt:       "Do something",
		ParentTaskID: &missing,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	err := s.CreateTask(ctx, child)
	if err == nil {
		t.Fatal("CreateTask succeeded with unknown parent_task_id, want FK violation")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("CreateTask error = %v, want foreign-key violation", err)
	}
}

func TestSQLite_DeleteTask_NullsChildParentTaskID(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	parent := &models.Task{
		ID:        "bf_PARENT_DEL",
		Status:    models.TaskStatusCompleted,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		Prompt:    "Parent task",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, parent); err != nil {
		t.Fatalf("CreateTask parent: %v", err)
	}

	parentID := parent.ID
	child := &models.Task{
		ID:           "bf_CHILD_DEL",
		Status:       models.TaskStatusPending,
		TaskMode:     models.TaskModeReview,
		Harness:      models.HarnessClaudeCode,
		Prompt:       "Child task",
		ParentTaskID: &parentID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.CreateTask(ctx, child); err != nil {
		t.Fatalf("CreateTask child: %v", err)
	}

	if err := s.DeleteTask(ctx, parent.ID); err != nil {
		t.Fatalf("DeleteTask parent: %v", err)
	}

	got, err := s.GetTask(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetTask child: %v", err)
	}
	if got.ParentTaskID != nil {
		t.Errorf("child ParentTaskID = %v, want nil after parent deletion", got.ParentTaskID)
	}
}

func TestSQLite_APIKeyRoundTrip(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expiresAt := now.Add(2 * time.Hour)

	key := &models.APIKey{
		KeyHash:     "hash-1",
		Name:        "integration-test",
		Permissions: []string{"tasks:read", "health:read"},
		ExpiresAt:   &expiresAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.CreateAPIKey(ctx, key); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	hasKeys, err := s.HasAPIKeys(ctx)
	if err != nil {
		t.Fatalf("HasAPIKeys: %v", err)
	}
	if !hasKeys {
		t.Fatal("HasAPIKeys returned false, want true")
	}

	got, err := s.GetAPIKeyByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if got.Name != key.Name {
		t.Fatalf("Name = %q, want %q", got.Name, key.Name)
	}
	if len(got.Permissions) != 2 || got.Permissions[0] != "tasks:read" || got.Permissions[1] != "health:read" {
		t.Fatalf("Permissions = %v, want [tasks:read health:read]", got.Permissions)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, expiresAt)
	}
}

func TestSQLite_WithTx_Commit(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &models.Task{
		ID:        "bf_TX01",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Prompt:    "Do something",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Transactional assign + start
	err := s.WithTx(ctx, func(tx Store) error {
		if err := tx.AssignTask(ctx, "bf_TX01"); err != nil {
			return err
		}
		return tx.StartTask(ctx, "bf_TX01", "container-tx01", "")
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	gotTask, _ := s.GetTask(ctx, "bf_TX01")
	if gotTask.Status != models.TaskStatusRunning {
		t.Errorf("Status = %q, want running", gotTask.Status)
	}
	if gotTask.ContainerID != "container-tx01" {
		t.Errorf("ContainerID = %q, want container-tx01", gotTask.ContainerID)
	}
}

func TestSQLite_WithTx_Rollback(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &models.Task{
		ID:        "bf_TX02",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Prompt:    "Do something",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Transaction that fails — the assign should roll back
	err := s.WithTx(ctx, func(tx Store) error {
		tx.AssignTask(ctx, "bf_TX02")
		tx.StartTask(ctx, "bf_TX02", "container-tx02", "")
		return fmt.Errorf("something failed")
	})
	if err == nil {
		t.Fatal("expected error from WithTx")
	}

	gotTask, _ := s.GetTask(ctx, "bf_TX02")
	if gotTask.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want pending (should have rolled back)", gotTask.Status)
	}
	if gotTask.ContainerID != "" {
		t.Errorf("ContainerID = %q, want empty (should have rolled back)", gotTask.ContainerID)
	}
}

// --- ErrNotFound ---

func TestSQLite_GetTaskNotFound(t *testing.T) {
	s := testSQLiteStore(t)
	got, err := s.GetTask(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent task")
	}
}

// --- ListTasks ---

func TestSQLite_ListTasks(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s)

	// Start the task so it becomes running
	s.StartTask(ctx, "bf_TEST001", "container-1", "")

	// List all
	tasks, err := s.ListTasks(ctx, TaskFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("ListTasks len = %d, want 1", len(tasks))
	}

	// Filter by pending — should be empty since we started it
	pending := models.TaskStatusPending
	tasks, _ = s.ListTasks(ctx, TaskFilter{Status: &pending})
	if len(tasks) != 0 {
		t.Errorf("ListTasks(pending) len = %d, want 0", len(tasks))
	}
}

// --- DeleteTask ---

func TestSQLite_DeleteTask(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s)

	if err := s.DeleteTask(ctx, "bf_TEST001"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}

	got, err := s.GetTask(ctx, "bf_TEST001")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

// --- Named task updates ---

func TestSQLite_UpdateTaskStatus(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	task := sqliteTestTask(t, s)

	if err := s.UpdateTaskStatus(ctx, task.ID, models.TaskStatusFailed, "something broke"); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}

	got, _ := s.GetTask(ctx, task.ID)
	if got.Status != models.TaskStatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusFailed)
	}
	if got.Error != "something broke" {
		t.Errorf("Error = %q, want %q", got.Error, "something broke")
	}
	// Verify other fields aren't clobbered
	if got.Prompt != "Fix the bug" {
		t.Errorf("Prompt was clobbered: %q", got.Prompt)
	}
	if !got.CreatePR {
		t.Error("CreatePR was clobbered")
	}
}

func TestSQLite_AssignTask(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s)

	if err := s.AssignTask(ctx, "bf_TEST001"); err != nil {
		t.Fatalf("AssignTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.Status != models.TaskStatusProvisioning {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusProvisioning)
	}
	if got.Prompt != "Fix the bug" {
		t.Errorf("Prompt was clobbered: %q", got.Prompt)
	}
}

func TestSQLite_StartTask(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s)

	if err := s.StartTask(ctx, "bf_TEST001", "container-abc", "skill-agent:v1"); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.Status != models.TaskStatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusRunning)
	}
	if got.ContainerID != "container-abc" {
		t.Errorf("ContainerID = %q, want %q", got.ContainerID, "container-abc")
	}
	if got.AgentImage != "skill-agent:v1" {
		t.Errorf("AgentImage = %q, want %q (StartTask must persist the routed image)", got.AgentImage, "skill-agent:v1")
	}
	if got.StartedAt == nil {
		t.Fatal("StartedAt should be set")
	}
	if time.Since(*got.StartedAt) > 5*time.Second {
		t.Errorf("StartedAt too old: %v", got.StartedAt)
	}
}

func TestSQLite_CompleteTask(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s)

	result := TaskResult{
		Status:         models.TaskStatusCompleted,
		PRURL:          "https://github.com/test/repo/pull/1",
		OutputURL:      "s3://bucket/output.log",
		CostUSD:        1.23,
		ElapsedTimeSec: 120,
	}
	if err := s.CompleteTask(ctx, "bf_TEST001", result); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.Status != models.TaskStatusCompleted {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusCompleted)
	}
	if got.PRURL != "https://github.com/test/repo/pull/1" {
		t.Errorf("PRURL = %q", got.PRURL)
	}
	if got.CostUSD != 1.23 {
		t.Errorf("CostUSD = %f, want 1.23", got.CostUSD)
	}
	if got.ElapsedTimeSec != 120 {
		t.Errorf("ElapsedTimeSec = %d, want 120", got.ElapsedTimeSec)
	}
	if got.CompletedAt == nil {
		t.Fatal("CompletedAt should be set")
	}
	if got.Prompt != "Fix the bug" {
		t.Errorf("Prompt was clobbered: %q", got.Prompt)
	}
}

func TestSQLite_CompleteTask_InferredFields(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s) // creates with RepoURL="https://github.com/test/repo", TaskMode="code"

	result := TaskResult{
		Status:       models.TaskStatusCompleted,
		RepoURL:      "https://github.com/inferred/repo",
		TargetBranch: "develop",
		TaskMode:     "code",
	}
	if err := s.CompleteTask(ctx, "bf_TEST001", result); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.RepoURL != "https://github.com/inferred/repo" {
		t.Errorf("RepoURL = %q, want %q", got.RepoURL, "https://github.com/inferred/repo")
	}
	if got.TargetBranch != "develop" {
		t.Errorf("TargetBranch = %q, want %q", got.TargetBranch, "develop")
	}
	if got.TaskMode != "code" {
		t.Errorf("TaskMode = %q, want %q", got.TaskMode, "code")
	}
}

func TestSQLite_CompleteTask_InferredFieldsCoalesce(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s) // creates with RepoURL="https://github.com/test/repo"

	// Complete with empty inferred fields — should NOT overwrite existing values
	result := TaskResult{
		Status: models.TaskStatusCompleted,
	}
	if err := s.CompleteTask(ctx, "bf_TEST001", result); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.RepoURL != "https://github.com/test/repo" {
		t.Errorf("RepoURL was clobbered: %q, want %q", got.RepoURL, "https://github.com/test/repo")
	}
	if got.TaskMode != models.TaskModeCode {
		t.Errorf("TaskMode was clobbered: %q, want %q", got.TaskMode, models.TaskModeCode)
	}
}

func TestSQLite_RequeueTask(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	task := sqliteTestTask(t, s)

	if _, err := s.q.ExecContext(ctx, "UPDATE tasks SET output_url=? WHERE id=?", "/api/v1/tasks/"+task.ID+"/output", task.ID); err != nil {
		t.Fatalf("seed output_url: %v", err)
	}

	s.AssignTask(ctx, task.ID)
	s.StartTask(ctx, task.ID, "container-abc", "")

	if err := s.RequeueTask(ctx, task.ID, "container gone"); err != nil {
		t.Fatalf("RequeueTask: %v", err)
	}

	got, _ := s.GetTask(ctx, task.ID)
	if got.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusPending)
	}
	if got.ContainerID != "" {
		t.Errorf("ContainerID should be cleared, got %q", got.ContainerID)
	}
	if got.StartedAt != nil {
		t.Error("StartedAt should be cleared")
	}
	if got.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", got.RetryCount)
	}
	if got.Error == "" {
		t.Error("Error should contain the reason")
	}
	if got.OutputURL != "" {
		t.Errorf("OutputURL should be cleared, got %q", got.OutputURL)
	}
}

func TestSQLite_CancelTask(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s)

	if err := s.CancelTask(ctx, "bf_TEST001"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.Status != models.TaskStatusCancelled {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusCancelled)
	}
	if got.CompletedAt == nil {
		t.Fatal("CompletedAt should be set")
	}
}

func TestSQLite_ClearTaskAssignment(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	sqliteTestTask(t, s)

	s.AssignTask(ctx, "bf_TEST001")
	s.StartTask(ctx, "bf_TEST001", "container-abc", "")

	if err := s.ClearTaskAssignment(ctx, "bf_TEST001"); err != nil {
		t.Fatalf("ClearTaskAssignment: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.ContainerID != "" {
		t.Errorf("ContainerID should be cleared, got %q", got.ContainerID)
	}
}

// --- Review task ---

func TestSQLite_ReviewTaskCRUD(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &models.Task{
		ID:           "bf_REVIEW01",
		Status:       models.TaskStatusPending,
		TaskMode:     models.TaskModeReview,
		RepoURL:      "https://github.com/test/repo",
		PRURL:        "https://github.com/test/repo/pull/42",
		Prompt:       "Focus on security",
		Model:        "claude-sonnet-4-6",
		MaxBudgetUSD: 5.0,
		MaxTurns:     50,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask(ctx, "bf_REVIEW01")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.TaskMode != models.TaskModeReview {
		t.Errorf("TaskMode = %q, want %q", got.TaskMode, models.TaskModeReview)
	}
	if got.PRURL != "https://github.com/test/repo/pull/42" {
		t.Errorf("PRURL = %q", got.PRURL)
	}
}

func TestSQLite_UpsertReading_Insert(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Need a task for the FK.
	task := sqliteTestTask(t, s)

	embedding := make([]float32, 1536)
	embedding[0] = 0.1
	embedding[1] = 0.9

	r := &models.Reading{
		ID:             "bf_READ001",
		TaskID:         task.ID,
		URL:            "https://example.com/article",
		Title:          "Test Article",
		TLDR:           "A short summary",
		Tags:           []string{"go", "testing"},
		Keywords:       []string{"tdd", "sqlite"},
		People:         []string{"Alice"},
		Orgs:           []string{"Acme"},
		NoveltyVerdict: "novel",
		Connections: []models.Connection{
			{ReadingID: "bf_READ000", Reason: "similar topic"},
		},
		Summary:   "A longer summary of the article.",
		RawOutput: []byte(`{"key":"value"}`),
		Embedding: embedding,
		CreatedAt: now,
	}

	if err := s.UpsertReading(ctx, r); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}

	// Read back via raw SQL to verify all fields.
	var (
		gotID, gotTaskID, gotURL, gotTitle, gotTLDR string
		gotNovelty, gotSummary                      string
		gotTags, gotKeywords, gotPeople, gotOrgs    string
		gotConnections, gotRawOutput                string
		gotEmbedding                                string
		gotCreatedAt                                string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, url, title, tldr,
		       tags, keywords, people, orgs,
		       novelty_verdict, connections, summary, raw_output,
		       embedding, created_at
		FROM readings WHERE id = ?`, r.ID).Scan(
		&gotID, &gotTaskID, &gotURL, &gotTitle, &gotTLDR,
		&gotTags, &gotKeywords, &gotPeople, &gotOrgs,
		&gotNovelty, &gotConnections, &gotSummary, &gotRawOutput,
		&gotEmbedding, &gotCreatedAt,
	)
	if err != nil {
		t.Fatalf("query reading back: %v", err)
	}

	if gotID != r.ID {
		t.Errorf("ID = %q, want %q", gotID, r.ID)
	}
	if gotTaskID != r.TaskID {
		t.Errorf("TaskID = %q, want %q", gotTaskID, r.TaskID)
	}
	if gotURL != r.URL {
		t.Errorf("URL = %q, want %q", gotURL, r.URL)
	}
	if gotTitle != r.Title {
		t.Errorf("Title = %q, want %q", gotTitle, r.Title)
	}
	if gotTLDR != r.TLDR {
		t.Errorf("TLDR = %q, want %q", gotTLDR, r.TLDR)
	}
	if gotNovelty != r.NoveltyVerdict {
		t.Errorf("NoveltyVerdict = %q, want %q", gotNovelty, r.NoveltyVerdict)
	}
	if gotSummary != r.Summary {
		t.Errorf("Summary = %q, want %q", gotSummary, r.Summary)
	}
	if gotTags != `["go","testing"]` {
		t.Errorf("Tags = %v, want %v", gotTags, r.Tags)
	}
	if gotKeywords != `["tdd","sqlite"]` {
		t.Errorf("Keywords = %v, want %v", gotKeywords, r.Keywords)
	}
	if gotPeople != `["Alice"]` {
		t.Errorf("People = %v, want %v", gotPeople, r.People)
	}
	if gotOrgs != `["Acme"]` {
		t.Errorf("Orgs = %v, want %v", gotOrgs, r.Orgs)
	}
	if gotCreatedAt != timeString(r.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", gotCreatedAt, r.CreatedAt)
	}
	// Verify embedding is non-empty (starts with "[0.1,0.9,")
	if gotEmbedding == "" || gotEmbedding[0] != '[' {
		t.Errorf("Embedding = %q, want non-empty vector", gotEmbedding)
	}
}

func TestSQLite_UpsertReading_Update(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := sqliteTestTask(t, s)

	embedding := make([]float32, 1536)
	embedding[0] = 0.3

	original := &models.Reading{
		ID:             "bf_READ003",
		TaskID:         task.ID,
		URL:            "https://example.com/updated",
		Title:          "Original Title",
		TLDR:           "Original TLDR",
		Tags:           []string{"v1"},
		Keywords:       []string{"old"},
		People:         []string{},
		Orgs:           []string{},
		NoveltyVerdict: "novel",
		Connections:    []models.Connection{},
		Summary:        "Original summary",
		RawOutput:      []byte(`{"v":1}`),
		Embedding:      embedding,
		CreatedAt:      now,
	}
	if err := s.UpsertReading(ctx, original); err != nil {
		t.Fatalf("UpsertReading (seed): %v", err)
	}

	// Upsert with same URL but different content and a new ID (force re-read).
	embedding[0] = 0.7
	updated := &models.Reading{
		ID:             "bf_READ004",
		TaskID:         task.ID,
		URL:            "https://example.com/updated",
		Title:          "Updated Title",
		TLDR:           "Updated TLDR",
		Tags:           []string{"v2"},
		Keywords:       []string{"new"},
		People:         []string{"Bob"},
		Orgs:           []string{"NewCo"},
		NoveltyVerdict: "not_novel",
		Connections:    []models.Connection{{ReadingID: "bf_READ001", Reason: "overlap"}},
		Summary:        "Updated summary",
		RawOutput:      []byte(`{"v":2}`),
		Embedding:      embedding,
		CreatedAt:      now,
	}
	if err := s.UpsertReading(ctx, updated); err != nil {
		t.Fatalf("UpsertReading (update): %v", err)
	}

	// Verify exactly one row for that URL.
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM readings WHERE url = ?", original.URL).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}

	// Verify the row has updated content but keeps the original ID.
	var gotID, gotTitle, gotTLDR, gotNovelty string
	err := s.db.QueryRowContext(ctx, "SELECT id, title, tldr, novelty_verdict FROM readings WHERE url = ?", original.URL).
		Scan(&gotID, &gotTitle, &gotTLDR, &gotNovelty)
	if err != nil {
		t.Fatalf("query after upsert-update: %v", err)
	}
	if gotID != original.ID {
		t.Errorf("ID = %q, want original %q (upsert should preserve ID)", gotID, original.ID)
	}
	if gotTitle != "Updated Title" {
		t.Errorf("Title = %q, want %q", gotTitle, "Updated Title")
	}
	if gotTLDR != "Updated TLDR" {
		t.Errorf("TLDR = %q, want %q", gotTLDR, "Updated TLDR")
	}
	if gotNovelty != "not_novel" {
		t.Errorf("NoveltyVerdict = %q, want %q", gotNovelty, "not_novel")
	}
}

func TestSQLite_GetReadingByURL(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := sqliteTestTask(t, s)

	embedding := make([]float32, 1536)
	embedding[0] = 0.42

	seeded := &models.Reading{
		ID:             "bf_READ_LOOKUP",
		TaskID:         task.ID,
		URL:            "https://example.com/lookup",
		Title:          "Lookup Target",
		TLDR:           "exact-url lookup",
		Tags:           []string{"lookup"},
		Keywords:       []string{},
		People:         []string{},
		Orgs:           []string{},
		NoveltyVerdict: "novel",
		Connections:    []models.Connection{},
		Summary:        "",
		RawOutput:      []byte(`{}`),
		Embedding:      embedding,
		CreatedAt:      now,
	}
	if err := s.UpsertReading(ctx, seeded); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}

	// Hit: exact URL match returns the seeded row.
	got, err := s.GetReadingByURL(ctx, "https://example.com/lookup")
	if err != nil {
		t.Fatalf("GetReadingByURL (hit): %v", err)
	}
	if got == nil {
		t.Fatal("GetReadingByURL: returned nil reading for hit")
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %q, want %q", got.ID, seeded.ID)
	}
	if got.URL != seeded.URL {
		t.Errorf("URL = %q, want %q", got.URL, seeded.URL)
	}
	if got.Title != seeded.Title {
		t.Errorf("Title = %q, want %q", got.Title, seeded.Title)
	}
	if got.TLDR != seeded.TLDR {
		t.Errorf("TLDR = %q, want %q", got.TLDR, seeded.TLDR)
	}

	// Miss: unknown URL returns ErrNotFound.
	_, err = s.GetReadingByURL(ctx, "https://example.com/does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetReadingByURL (miss) err = %v, want ErrNotFound", err)
	}
}

func TestSQLite_UpsertReading_RoundTripsContentFields(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := sqliteTestTask(t, s)

	fetchedAt := now.Add(-3 * time.Second)
	seeded := &models.Reading{
		ID:             "bf_READ_CONTENT",
		TaskID:         task.ID,
		URL:            "https://example.com/content-roundtrip",
		Title:          "Round-trip target",
		TLDR:           "verifies content columns persist",
		Tags:           []string{"content"},
		Keywords:       []string{},
		People:         []string{},
		Orgs:           []string{},
		NoveltyVerdict: "novel",
		Connections:    []models.Connection{},
		Summary:        "",
		RawOutput:      []byte(`{}`),
		CreatedAt:      now,

		ContentType:    "text/html; charset=utf-8",
		ContentStatus:  "captured",
		ContentBytes:   1234,
		ExtractedBytes: 567,
		ContentSHA256:  "deadbeefcafef00d",
		FetchedAt:      &fetchedAt,
	}
	if err := s.UpsertReading(ctx, seeded); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}

	got, err := s.GetReadingByURL(ctx, seeded.URL)
	if err != nil {
		t.Fatalf("GetReadingByURL: %v", err)
	}
	if got.ContentType != seeded.ContentType {
		t.Errorf("ContentType = %q, want %q", got.ContentType, seeded.ContentType)
	}
	if got.ContentStatus != seeded.ContentStatus {
		t.Errorf("ContentStatus = %q, want %q", got.ContentStatus, seeded.ContentStatus)
	}
	if got.ContentBytes != seeded.ContentBytes {
		t.Errorf("ContentBytes = %d, want %d", got.ContentBytes, seeded.ContentBytes)
	}
	if got.ExtractedBytes != seeded.ExtractedBytes {
		t.Errorf("ExtractedBytes = %d, want %d", got.ExtractedBytes, seeded.ExtractedBytes)
	}
	if got.ContentSHA256 != seeded.ContentSHA256 {
		t.Errorf("ContentSHA256 = %q, want %q", got.ContentSHA256, seeded.ContentSHA256)
	}
	if got.FetchedAt == nil || !got.FetchedAt.Equal(fetchedAt) {
		t.Errorf("FetchedAt = %v, want %v", got.FetchedAt, fetchedAt)
	}

	// Legacy/empty content path: zero-value content fields round-trip too.
	legacy := &models.Reading{
		ID:             "bf_READ_LEGACY",
		TaskID:         task.ID,
		URL:            "https://example.com/legacy",
		Tags:           []string{},
		Keywords:       []string{},
		People:         []string{},
		Orgs:           []string{},
		Connections:    []models.Connection{},
		NoveltyVerdict: "novel",
		RawOutput:      []byte(`{}`),
		CreatedAt:      now,
	}
	if err := s.UpsertReading(ctx, legacy); err != nil {
		t.Fatalf("UpsertReading legacy: %v", err)
	}
	gotLegacy, err := s.GetReadingByURL(ctx, legacy.URL)
	if err != nil {
		t.Fatalf("GetReadingByURL legacy: %v", err)
	}
	if gotLegacy.ContentStatus != "" {
		t.Errorf("legacy ContentStatus = %q, want empty", gotLegacy.ContentStatus)
	}
	if gotLegacy.ContentBytes != 0 {
		t.Errorf("legacy ContentBytes = %d, want 0", gotLegacy.ContentBytes)
	}
	if gotLegacy.FetchedAt != nil {
		t.Errorf("legacy FetchedAt = %v, want nil", gotLegacy.FetchedAt)
	}
}

func TestSQLite_ListReadings_NewestFirstWithPagination(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	task := sqliteTestTask(t, s)
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	for i, seed := range []struct {
		id        string
		url       string
		title     string
		createdAt time.Time
	}{
		{"bf_READ_OLD", "https://example.com/old", "Oldest", base},
		{"bf_READ_NEW", "https://example.com/new", "Newest", base.Add(2 * time.Hour)},
		{"bf_READ_MID", "https://example.com/mid", "Middle", base.Add(time.Hour)},
	} {
		r := &models.Reading{
			ID:          seed.id,
			TaskID:      task.ID,
			URL:         seed.url,
			Title:       seed.title,
			TLDR:        fmt.Sprintf("summary %d", i),
			Tags:        []string{},
			Keywords:    []string{},
			People:      []string{},
			Orgs:        []string{},
			Connections: []models.Connection{},
			RawOutput:   []byte(`{}`),
			Embedding:   []float32{float32(i + 1)},
			CreatedAt:   seed.createdAt,
		}
		if err := s.UpsertReading(ctx, r); err != nil {
			t.Fatalf("UpsertReading %s: %v", seed.id, err)
		}
	}

	got, err := s.ListReadings(ctx, ReadingFilter{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("ListReadings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d readings, want 2", len(got))
	}
	if got[0].ID != "bf_READ_MID" || got[1].ID != "bf_READ_OLD" {
		t.Fatalf("reading order = [%s %s], want [bf_READ_MID bf_READ_OLD]", got[0].ID, got[1].ID)
	}
	if got[0].Embedding != nil {
		t.Fatalf("list result exposed embedding vector, want nil")
	}
}

func TestSQLite_GetReadingByID(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := sqliteTestTask(t, s)

	seeded := &models.Reading{
		ID:             "bf_READ_DETAIL",
		TaskID:         task.ID,
		URL:            "https://example.com/detail",
		Title:          "Detail Target",
		TLDR:           "detail tldr",
		Tags:           []string{"systems", "ai"},
		Keywords:       []string{"sqlite"},
		People:         []string{"Ada"},
		Orgs:           []string{"Backlite"},
		NoveltyVerdict: "new",
		Connections: []models.Connection{
			{ReadingID: "bf_READ_OTHER", Reason: "same deployment topic"},
		},
		Summary:   "Full detail summary.",
		RawOutput: []byte(`{"debug":true}`),
		Embedding: []float32{0.2, 0.8},
		CreatedAt: now,
	}
	if err := s.UpsertReading(ctx, seeded); err != nil {
		t.Fatalf("UpsertReading: %v", err)
	}

	got, err := s.GetReading(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("GetReading: %v", err)
	}
	if got.ID != seeded.ID || got.URL != seeded.URL || got.Summary != seeded.Summary {
		t.Fatalf("GetReading returned %#v, want seeded detail", got)
	}
	if len(got.Tags) != 2 || got.Tags[1] != "ai" {
		t.Fatalf("Tags = %v, want [systems ai]", got.Tags)
	}
	if len(got.People) != 1 || got.People[0] != "Ada" {
		t.Fatalf("People = %v, want [Ada]", got.People)
	}
	if got.Embedding != nil {
		t.Fatalf("detail result exposed embedding vector, want nil")
	}

	_, err = s.GetReading(ctx, "bf_READ_MISSING")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReading missing err = %v, want ErrNotFound", err)
	}
}

func TestSQLite_MatchReadings_SimilarityOrdering(t *testing.T) {
	s := testSQLiteStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := sqliteTestTask(t, s)

	// Create 3 readings with unit vectors along different dimensions.
	// Reading A: dimension 0
	// Reading B: dimension 1
	// Reading C: dimension 2
	makeEmbedding := func(dim int) []float32 {
		v := make([]float32, 1536)
		v[dim] = 1.0
		return v
	}

	readings := []struct {
		id    string
		url   string
		title string
		dim   int
	}{
		{"bf_SIM_A", "https://example.com/a", "Article A", 0},
		{"bf_SIM_B", "https://example.com/b", "Article B", 1},
		{"bf_SIM_C", "https://example.com/c", "Article C", 2},
	}
	for _, rd := range readings {
		r := &models.Reading{
			ID:          rd.id,
			TaskID:      task.ID,
			URL:         rd.url,
			Title:       rd.title,
			Tags:        []string{},
			Keywords:    []string{},
			People:      []string{},
			Orgs:        []string{},
			Connections: []models.Connection{},
			RawOutput:   []byte(`{}`),
			Embedding:   makeEmbedding(rd.dim),
			CreatedAt:   now,
		}
		if err := s.UpsertReading(ctx, r); err != nil {
			t.Fatalf("UpsertReading %s: %v", rd.id, err)
		}
	}

	// Query with a vector close to dimension 0 (should rank A first).
	query := makeEmbedding(0)
	query[1] = 0.1 // slight component toward B

	results, err := s.FindSimilarReadings(ctx, query, 3)
	if err != nil {
		t.Fatalf("FindSimilarReadings: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// A should be most similar (closest to dim 0).
	if results[0].ID != "bf_SIM_A" {
		t.Errorf("rank 1 = %q, want bf_SIM_A", results[0].ID)
	}
	// B should be second (slight component in dim 1).
	if results[1].ID != "bf_SIM_B" {
		t.Errorf("rank 2 = %q, want bf_SIM_B", results[1].ID)
	}
	// C should be last.
	if results[2].ID != "bf_SIM_C" {
		t.Errorf("rank 3 = %q, want bf_SIM_C", results[2].ID)
	}

	// Similarities should be monotonically decreasing.
	for i := 1; i < len(results); i++ {
		if results[i].Similarity >= results[i-1].Similarity {
			t.Errorf("similarity[%d] = %f >= similarity[%d] = %f, want decreasing",
				i, results[i].Similarity, i-1, results[i-1].Similarity)
		}
	}
}
