//go:build !nocontainers

package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pgTestTask creates a minimal task and inserts it.
func pgTestTask(t *testing.T, s *PostgresStore) *models.Task {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task := &models.Task{
		ID:        "bf_TEST001",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Branch:    "backflow/test",
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

// pgTestInstance creates a minimal instance and inserts it.
func pgTestInstance(t *testing.T, s *PostgresStore) *models.Instance {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
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
	return inst
}

var sharedConnStr string

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("backflow_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
	)
	if err != nil {
		log.Fatalf("start postgres container: %v", err)
	}

	sharedConnStr, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get connection string: %v", err)
	}

	// Run migrations once.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	s, err := NewPostgres(ctx, sharedConnStr, migrationsDir)
	if err != nil {
		log.Fatalf("NewPostgres: %v", err)
	}
	s.Close()

	code := m.Run()

	pgContainer.Terminate(ctx)
	os.Exit(code)
}

func testPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, sharedConnStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	s := &PostgresStore{pool: pool, q: pool}

	// Clean slate for test isolation.
	if _, err := s.pool.Exec(ctx, "TRUNCATE tasks, instances, allowed_senders, discord_installs, discord_task_threads CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func TestPG_TaskRoundTrip(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	task := &models.Task{
		ID:              "bf_TEST001",
		Status:          models.TaskStatusPending,
		TaskMode:        models.TaskModeCode,
		Harness:         models.HarnessClaudeCode,
		RepoURL:         "https://github.com/test/repo",
		Branch:          "backflow/test",
		TargetBranch:    "main",
		Prompt:          "Fix the bug",
		Model:           "claude-sonnet-4-6",
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
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}
}

func TestPG_WithTx_Commit(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Seed a task and instance
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

	inst := &models.Instance{
		InstanceID:   "i-tx01",
		InstanceType: "m7g.xlarge",
		Status:       models.InstanceStatusRunning,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// Transactional assign + increment
	err := s.WithTx(ctx, func(tx Store) error {
		if err := tx.AssignTask(ctx, "bf_TX01", "i-tx01"); err != nil {
			return err
		}
		return tx.IncrementRunningContainers(ctx, "i-tx01")
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	// Verify both took effect
	gotTask, _ := s.GetTask(ctx, "bf_TX01")
	if gotTask.Status != models.TaskStatusProvisioning {
		t.Errorf("Status = %q, want provisioning", gotTask.Status)
	}
	if gotTask.InstanceID != "i-tx01" {
		t.Errorf("InstanceID = %q, want i-tx01", gotTask.InstanceID)
	}

	gotInst, _ := s.GetInstance(ctx, "i-tx01")
	if gotInst.RunningContainers != 1 {
		t.Errorf("RunningContainers = %d, want 1", gotInst.RunningContainers)
	}
}

func TestPG_WithTx_Rollback(t *testing.T) {
	s := testPostgresStore(t)
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

	inst := &models.Instance{
		InstanceID:   "i-tx02",
		InstanceType: "m7g.xlarge",
		Status:       models.InstanceStatusRunning,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.CreateInstance(ctx, inst); err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}

	// Transaction that fails — both operations should roll back
	err := s.WithTx(ctx, func(tx Store) error {
		tx.AssignTask(ctx, "bf_TX02", "i-tx02")
		tx.IncrementRunningContainers(ctx, "i-tx02")
		return fmt.Errorf("something failed")
	})
	if err == nil {
		t.Fatal("expected error from WithTx")
	}

	// Both should be unchanged
	gotTask, _ := s.GetTask(ctx, "bf_TX02")
	if gotTask.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want pending (should have rolled back)", gotTask.Status)
	}

	gotInst, _ := s.GetInstance(ctx, "i-tx02")
	if gotInst.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0 (should have rolled back)", gotInst.RunningContainers)
	}
}

// --- ErrNotFound ---

func TestPG_GetTaskNotFound(t *testing.T) {
	s := testPostgresStore(t)
	got, err := s.GetTask(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent task")
	}
}

func TestPG_GetInstanceNotFound(t *testing.T) {
	s := testPostgresStore(t)
	got, err := s.GetInstance(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent instance")
	}
}

// --- ListTasks ---

func TestPG_ListTasks(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s)

	// Start the task so it becomes running
	s.StartTask(ctx, "bf_TEST001", "container-1")

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

func TestPG_DeleteTask(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s)

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

func TestPG_UpdateTaskStatus(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	task := pgTestTask(t, s)

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

func TestPG_AssignTask(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s)

	if err := s.AssignTask(ctx, "bf_TEST001", "i-abc123"); err != nil {
		t.Fatalf("AssignTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.Status != models.TaskStatusProvisioning {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusProvisioning)
	}
	if got.InstanceID != "i-abc123" {
		t.Errorf("InstanceID = %q, want %q", got.InstanceID, "i-abc123")
	}
	if got.Prompt != "Fix the bug" {
		t.Errorf("Prompt was clobbered: %q", got.Prompt)
	}
}

func TestPG_StartTask(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s)

	if err := s.StartTask(ctx, "bf_TEST001", "container-abc"); err != nil {
		t.Fatalf("StartTask: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.Status != models.TaskStatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusRunning)
	}
	if got.ContainerID != "container-abc" {
		t.Errorf("ContainerID = %q, want %q", got.ContainerID, "container-abc")
	}
	if got.StartedAt == nil {
		t.Fatal("StartedAt should be set")
	}
	if time.Since(*got.StartedAt) > 5*time.Second {
		t.Errorf("StartedAt too old: %v", got.StartedAt)
	}
}

func TestPG_CompleteTask(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s)

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

func TestPG_CompleteTask_InferredFields(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s) // creates with RepoURL="https://github.com/test/repo", TaskMode="code"

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

func TestPG_CompleteTask_InferredFieldsCoalesce(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s) // creates with RepoURL="https://github.com/test/repo"

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

func TestPG_RequeueTask(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	task := pgTestTask(t, s)

	s.AssignTask(ctx, task.ID, "i-abc123")
	s.StartTask(ctx, task.ID, "container-abc")

	if err := s.RequeueTask(ctx, task.ID, "instance terminated"); err != nil {
		t.Fatalf("RequeueTask: %v", err)
	}

	got, _ := s.GetTask(ctx, task.ID)
	if got.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want %q", got.Status, models.TaskStatusPending)
	}
	if got.InstanceID != "" {
		t.Errorf("InstanceID should be cleared, got %q", got.InstanceID)
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
}

func TestPG_CancelTask(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s)

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

func TestPG_ClearTaskAssignment(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestTask(t, s)

	s.AssignTask(ctx, "bf_TEST001", "i-abc123")
	s.StartTask(ctx, "bf_TEST001", "container-abc")

	if err := s.ClearTaskAssignment(ctx, "bf_TEST001"); err != nil {
		t.Fatalf("ClearTaskAssignment: %v", err)
	}

	got, _ := s.GetTask(ctx, "bf_TEST001")
	if got.InstanceID != "" {
		t.Errorf("InstanceID should be cleared, got %q", got.InstanceID)
	}
	if got.ContainerID != "" {
		t.Errorf("ContainerID should be cleared, got %q", got.ContainerID)
	}
}

// --- Instance CRUD ---

func TestPG_InstanceCRUD(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestInstance(t, s)

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

	// List filtered
	running := models.InstanceStatusRunning
	instances, err := s.ListInstances(ctx, &running)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Errorf("ListInstances len = %d, want 1", len(instances))
	}
}

// --- Named instance updates ---

func TestPG_UpdateInstanceStatus(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestInstance(t, s)

	s.IncrementRunningContainers(ctx, "i-test123")

	if err := s.UpdateInstanceStatus(ctx, "i-test123", models.InstanceStatusTerminated); err != nil {
		t.Fatalf("UpdateInstanceStatus: %v", err)
	}

	got, _ := s.GetInstance(ctx, "i-test123")
	if got.Status != models.InstanceStatusTerminated {
		t.Errorf("Status = %q, want %q", got.Status, models.InstanceStatusTerminated)
	}
	if got.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0 (should zero on terminate)", got.RunningContainers)
	}
	if got.InstanceType != "m7g.xlarge" {
		t.Errorf("InstanceType was clobbered: %q", got.InstanceType)
	}
}

func TestPG_IncrementDecrementRunningContainers(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	pgTestInstance(t, s)

	s.IncrementRunningContainers(ctx, "i-test123")
	got, _ := s.GetInstance(ctx, "i-test123")
	if got.RunningContainers != 1 {
		t.Errorf("RunningContainers = %d, want 1", got.RunningContainers)
	}

	s.IncrementRunningContainers(ctx, "i-test123")
	got, _ = s.GetInstance(ctx, "i-test123")
	if got.RunningContainers != 2 {
		t.Errorf("RunningContainers = %d, want 2", got.RunningContainers)
	}

	s.DecrementRunningContainers(ctx, "i-test123")
	got, _ = s.GetInstance(ctx, "i-test123")
	if got.RunningContainers != 1 {
		t.Errorf("RunningContainers = %d, want 1", got.RunningContainers)
	}

	// Floor at zero
	s.DecrementRunningContainers(ctx, "i-test123")
	s.DecrementRunningContainers(ctx, "i-test123")
	got, _ = s.GetInstance(ctx, "i-test123")
	if got.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0 (should floor at zero)", got.RunningContainers)
	}
}

func TestPG_UpdateInstanceDetails(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	inst := &models.Instance{
		InstanceID:   "i-new",
		InstanceType: "m7g.xlarge",
		Status:       models.InstanceStatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.CreateInstance(ctx, inst)

	if err := s.UpdateInstanceDetails(ctx, "i-new", "10.0.1.99", "us-west-2b"); err != nil {
		t.Fatalf("UpdateInstanceDetails: %v", err)
	}

	got, _ := s.GetInstance(ctx, "i-new")
	if got.PrivateIP != "10.0.1.99" {
		t.Errorf("PrivateIP = %q, want 10.0.1.99", got.PrivateIP)
	}
	if got.AvailabilityZone != "us-west-2b" {
		t.Errorf("AvailabilityZone = %q, want us-west-2b", got.AvailabilityZone)
	}
}

func TestPG_ResetRunningContainers(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	inst := pgTestInstance(t, s)

	s.IncrementRunningContainers(ctx, inst.InstanceID)
	s.IncrementRunningContainers(ctx, inst.InstanceID)

	if err := s.ResetRunningContainers(ctx, inst.InstanceID); err != nil {
		t.Fatalf("ResetRunningContainers: %v", err)
	}

	got, _ := s.GetInstance(ctx, inst.InstanceID)
	if got.RunningContainers != 0 {
		t.Errorf("RunningContainers = %d, want 0", got.RunningContainers)
	}
}

// --- AllowedSender ---

func TestPG_CreateAllowedSender(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()

	sender := &models.AllowedSender{
		ChannelType: "sms",
		Address:     "+15551234567",
		Enabled:     true,
		CreatedAt:   time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.CreateAllowedSender(ctx, sender); err != nil {
		t.Fatalf("CreateAllowedSender: %v", err)
	}

	got, err := s.GetAllowedSender(ctx, "sms", "+15551234567")
	if err != nil {
		t.Fatalf("GetAllowedSender: %v", err)
	}
	if got.ChannelType != "sms" {
		t.Errorf("ChannelType = %q, want sms", got.ChannelType)
	}
	if got.Address != "+15551234567" {
		t.Errorf("Address = %q", got.Address)
	}
	if !got.Enabled {
		t.Error("Enabled should be true")
	}
}

// --- Review task ---

func TestPG_ReviewTaskCRUD(t *testing.T) {
	s := testPostgresStore(t)
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

func TestPG_UpsertAndGetDiscordInstall(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	install := &models.DiscordInstall{
		GuildID:      "guild-123",
		AppID:        "app-456",
		ChannelID:    "channel-789",
		AllowedRoles: []string{"role-a", "role-b"},
		InstalledAt:  now,
		UpdatedAt:    now,
	}

	if err := s.UpsertDiscordInstall(ctx, install); err != nil {
		t.Fatalf("UpsertDiscordInstall: %v", err)
	}

	got, err := s.GetDiscordInstall(ctx, "guild-123")
	if err != nil {
		t.Fatalf("GetDiscordInstall: %v", err)
	}
	if got.GuildID != "guild-123" {
		t.Errorf("GuildID = %q, want %q", got.GuildID, "guild-123")
	}
	if got.AppID != "app-456" {
		t.Errorf("AppID = %q, want %q", got.AppID, "app-456")
	}
	if got.ChannelID != "channel-789" {
		t.Errorf("ChannelID = %q, want %q", got.ChannelID, "channel-789")
	}
	if len(got.AllowedRoles) != 2 || got.AllowedRoles[0] != "role-a" || got.AllowedRoles[1] != "role-b" {
		t.Errorf("AllowedRoles = %v, want [role-a role-b]", got.AllowedRoles)
	}
	if !got.InstalledAt.Equal(now) {
		t.Errorf("InstalledAt = %v, want %v", got.InstalledAt, now)
	}

	// Upsert with changed channel — should update, not duplicate
	updated := time.Now().UTC().Truncate(time.Microsecond)
	install.ChannelID = "channel-new"
	install.UpdatedAt = updated
	if err := s.UpsertDiscordInstall(ctx, install); err != nil {
		t.Fatalf("UpsertDiscordInstall (update): %v", err)
	}
	got, err = s.GetDiscordInstall(ctx, "guild-123")
	if err != nil {
		t.Fatalf("GetDiscordInstall after update: %v", err)
	}
	if got.ChannelID != "channel-new" {
		t.Errorf("ChannelID after update = %q, want %q", got.ChannelID, "channel-new")
	}
	if !got.UpdatedAt.Equal(updated) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, updated)
	}
	// InstalledAt should not change on upsert
	if !got.InstalledAt.Equal(now) {
		t.Errorf("InstalledAt changed after update: got %v, want %v", got.InstalledAt, now)
	}
}

func TestPG_GetDiscordInstall_NotFound(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()

	_, err := s.GetDiscordInstall(ctx, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDiscordInstall = %v, want ErrNotFound", err)
	}
}

func TestPG_DeleteDiscordInstall(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	install := &models.DiscordInstall{
		GuildID:     "guild-del",
		AppID:       "app-del",
		ChannelID:   "ch-del",
		InstalledAt: now,
		UpdatedAt:   now,
	}
	if err := s.UpsertDiscordInstall(ctx, install); err != nil {
		t.Fatalf("UpsertDiscordInstall: %v", err)
	}

	if err := s.DeleteDiscordInstall(ctx, "guild-del"); err != nil {
		t.Fatalf("DeleteDiscordInstall: %v", err)
	}

	_, err := s.GetDiscordInstall(ctx, "guild-del")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDiscordInstall after delete = %v, want ErrNotFound", err)
	}
}

func TestPG_UpsertAndGetDiscordTaskThread(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create a parent task so the FK is satisfied.
	task := &models.Task{
		ID:        "bf_thread_1",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/test/repo",
		Prompt:    "test",
		Model:     "claude-sonnet-4-6",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	thread := &models.DiscordTaskThread{
		TaskID:        "bf_thread_1",
		RootMessageID: "root-123",
		ThreadID:      "thread-123",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.UpsertDiscordTaskThread(ctx, thread); err != nil {
		t.Fatalf("UpsertDiscordTaskThread: %v", err)
	}

	got, err := s.GetDiscordTaskThread(ctx, "bf_thread_1")
	if err != nil {
		t.Fatalf("GetDiscordTaskThread: %v", err)
	}
	if got.TaskID != thread.TaskID {
		t.Errorf("TaskID = %q, want %q", got.TaskID, thread.TaskID)
	}
	if got.RootMessageID != thread.RootMessageID {
		t.Errorf("RootMessageID = %q, want %q", got.RootMessageID, thread.RootMessageID)
	}
	if got.ThreadID != thread.ThreadID {
		t.Errorf("ThreadID = %q, want %q", got.ThreadID, thread.ThreadID)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, now)
	}

	updated := time.Now().UTC().Truncate(time.Microsecond)
	thread.RootMessageID = "root-456"
	thread.ThreadID = "thread-456"
	thread.UpdatedAt = updated
	if err := s.UpsertDiscordTaskThread(ctx, thread); err != nil {
		t.Fatalf("UpsertDiscordTaskThread (update): %v", err)
	}

	got, err = s.GetDiscordTaskThread(ctx, "bf_thread_1")
	if err != nil {
		t.Fatalf("GetDiscordTaskThread after update: %v", err)
	}
	if got.RootMessageID != "root-456" {
		t.Errorf("RootMessageID after update = %q, want %q", got.RootMessageID, "root-456")
	}
	if got.ThreadID != "thread-456" {
		t.Errorf("ThreadID after update = %q, want %q", got.ThreadID, "thread-456")
	}
	if !got.UpdatedAt.Equal(updated) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, updated)
	}
}

func TestPG_GetDiscordTaskThread_NotFound(t *testing.T) {
	s := testPostgresStore(t)
	ctx := context.Background()

	_, err := s.GetDiscordTaskThread(ctx, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDiscordTaskThread = %v, want ErrNotFound", err)
	}
}
