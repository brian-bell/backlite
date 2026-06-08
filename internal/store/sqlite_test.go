package store

import (
	"context"
	"database/sql"
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

func TestMigration005_PreservesParentTaskLinks(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pre-read-removal.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE goose_db_version (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version_id INTEGER NOT NULL,
			is_applied INTEGER NOT NULL,
			tstamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO goose_db_version(version_id, is_applied)
		VALUES (1, 1), (2, 1), (3, 1), (4, 1);

		CREATE TABLE tasks (
			id                TEXT PRIMARY KEY,
			status            TEXT NOT NULL DEFAULT 'pending',
			task_mode         TEXT NOT NULL DEFAULT 'auto',
			harness           TEXT NOT NULL DEFAULT 'claude_code',
			repo_url          TEXT NOT NULL DEFAULT '',
			branch            TEXT NOT NULL DEFAULT '',
			target_branch     TEXT NOT NULL DEFAULT '',
			prompt            TEXT NOT NULL,
			context           TEXT NOT NULL DEFAULT '',
			model             TEXT NOT NULL DEFAULT '',
			effort            TEXT NOT NULL DEFAULT '',
			max_budget_usd    REAL NOT NULL DEFAULT 0,
			max_runtime_sec   INTEGER NOT NULL DEFAULT 0,
			max_turns         INTEGER NOT NULL DEFAULT 0,
			create_pr         BOOLEAN NOT NULL DEFAULT false,
			self_review       BOOLEAN NOT NULL DEFAULT false,
			save_agent_output BOOLEAN NOT NULL DEFAULT true,
			pr_title          TEXT NOT NULL DEFAULT '',
			pr_body           TEXT NOT NULL DEFAULT '',
			pr_url            TEXT NOT NULL DEFAULT '',
			output_url        TEXT NOT NULL DEFAULT '',
			allowed_tools     TEXT NOT NULL DEFAULT '[]',
			claude_md         TEXT NOT NULL DEFAULT '',
			env_vars          TEXT NOT NULL DEFAULT '{}',
			container_id      TEXT NOT NULL DEFAULT '',
			retry_count       INTEGER NOT NULL DEFAULT 0,
			user_retry_count  INTEGER NOT NULL DEFAULT 0,
			cost_usd          REAL NOT NULL DEFAULT 0,
			elapsed_time_sec  INTEGER NOT NULL DEFAULT 0,
			error             TEXT NOT NULL DEFAULT '',
			ready_for_retry   BOOLEAN NOT NULL DEFAULT false,
			agent_image       TEXT NOT NULL DEFAULT '',
			force             BOOLEAN NOT NULL DEFAULT false,
			parent_task_id    TEXT REFERENCES tasks(id) ON DELETE SET NULL,
			inline_content_sha256 TEXT NULL,
			created_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			updated_at        TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			started_at        TEXT,
			completed_at      TEXT
		);
		CREATE INDEX idx_tasks_status ON tasks(status);
		CREATE INDEX idx_tasks_created ON tasks(created_at);
		CREATE INDEX idx_tasks_parent_task_id ON tasks(parent_task_id);

		CREATE TABLE readings (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
			url TEXT NOT NULL
		);
		CREATE UNIQUE INDEX idx_readings_url ON readings(url);

		INSERT INTO tasks(id, status, task_mode, harness, prompt)
		VALUES ('bf_PARENT_LINK', 'completed', 'code', 'claude_code', 'parent');
		INSERT INTO tasks(id, status, task_mode, harness, prompt, parent_task_id)
		VALUES ('bf_CHILD_LINK', 'completed', 'review', 'claude_code', 'child', 'bf_PARENT_LINK');
		INSERT INTO tasks(id, status, task_mode, harness, prompt)
		VALUES ('bf_READ_OLD', 'pending', 'read', 'claude_code', 'https://example.com/old');
		INSERT INTO tasks(id, status, task_mode, harness, prompt, parent_task_id)
		VALUES ('bf_CHILD_OF_READ', 'completed', 'review', 'claude_code', 'review child', 'bf_READ_OLD');
	`); err != nil {
		db.Close()
		t.Fatalf("seed pre-migration db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	s, err := NewSQLite(ctx, dbPath, filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close store: %v", err)
		}
	})

	child, err := s.GetTask(ctx, "bf_CHILD_LINK")
	if err != nil {
		t.Fatalf("GetTask child: %v", err)
	}
	if child.ParentTaskID == nil || *child.ParentTaskID != "bf_PARENT_LINK" {
		t.Fatalf("ParentTaskID = %v, want bf_PARENT_LINK", child.ParentTaskID)
	}

	if _, err := s.GetTask(ctx, "bf_READ_OLD"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read task lookup err = %v, want ErrNotFound", err)
	}
	childOfRead, err := s.GetTask(ctx, "bf_CHILD_OF_READ")
	if err != nil {
		t.Fatalf("GetTask child of read: %v", err)
	}
	if childOfRead.ParentTaskID != nil {
		t.Fatalf("child of dropped read task ParentTaskID = %v, want nil", childOfRead.ParentTaskID)
	}

	var fkTarget string
	if err := s.db.QueryRowContext(ctx, "SELECT [table] FROM pragma_foreign_key_list('tasks') WHERE [from] = 'parent_task_id'").Scan(&fkTarget); err != nil {
		t.Fatalf("query parent_task_id foreign key: %v", err)
	}
	if fkTarget != "tasks" {
		t.Fatalf("parent_task_id FK target = %q, want tasks", fkTarget)
	}
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
