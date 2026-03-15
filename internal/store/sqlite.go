package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/backflow-labs/backflow/internal/models"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLite(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id              TEXT PRIMARY KEY,
		status          TEXT NOT NULL DEFAULT 'pending',
		harness         TEXT NOT NULL DEFAULT 'claude_code',
		repo_url        TEXT NOT NULL,
		branch          TEXT NOT NULL DEFAULT '',
		target_branch   TEXT NOT NULL DEFAULT '',
		prompt          TEXT NOT NULL,
		context         TEXT NOT NULL DEFAULT '',
		model           TEXT NOT NULL DEFAULT '',
		effort          TEXT NOT NULL DEFAULT '',
		max_budget_usd  REAL NOT NULL DEFAULT 0,
		max_runtime_min INTEGER NOT NULL DEFAULT 0,
		max_turns       INTEGER NOT NULL DEFAULT 0,
		create_pr       INTEGER NOT NULL DEFAULT 0,
		self_review     INTEGER NOT NULL DEFAULT 0,
		pr_title        TEXT NOT NULL DEFAULT '',
		pr_body         TEXT NOT NULL DEFAULT '',
		pr_url          TEXT NOT NULL DEFAULT '',
		allowed_tools   TEXT NOT NULL DEFAULT '[]',
		claude_md       TEXT NOT NULL DEFAULT '',
		env_vars        TEXT NOT NULL DEFAULT '{}',
		instance_id     TEXT NOT NULL DEFAULT '',
		container_id    TEXT NOT NULL DEFAULT '',
		retry_count     INTEGER NOT NULL DEFAULT 0,
		cost_usd        REAL NOT NULL DEFAULT 0,
		error           TEXT NOT NULL DEFAULT '',
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		started_at      TEXT,
		completed_at    TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
	CREATE INDEX IF NOT EXISTS idx_tasks_created ON tasks(created_at);

	CREATE TABLE IF NOT EXISTS instances (
		instance_id        TEXT PRIMARY KEY,
		instance_type      TEXT NOT NULL,
		availability_zone  TEXT NOT NULL DEFAULT '',
		private_ip         TEXT NOT NULL DEFAULT '',
		status             TEXT NOT NULL DEFAULT 'pending',
		max_containers     INTEGER NOT NULL DEFAULT 4,
		running_containers INTEGER NOT NULL DEFAULT 0,
		created_at         TEXT NOT NULL,
		updated_at         TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_instances_status ON instances(status);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Idempotent migrations for new columns
	migrations := []string{
		"ALTER TABLE tasks ADD COLUMN task_mode TEXT NOT NULL DEFAULT 'code'",
		"ALTER TABLE tasks ADD COLUMN review_pr_number INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE tasks ADD COLUMN harness TEXT NOT NULL DEFAULT 'claude_code'",
	}
	for _, m := range migrations {
		s.db.Exec(m) // ignore "duplicate column" errors
	}
	return nil
}

// --- Tasks ---

func (s *SQLiteStore) CreateTask(ctx context.Context, task *models.Task) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (
			id, status, task_mode, harness, repo_url, branch, target_branch, review_pr_number,
			prompt, context,
			model, effort, max_budget_usd, max_runtime_min, max_turns,
			create_pr, self_review, pr_title, pr_body, pr_url,
			allowed_tools, claude_md, env_vars,
			instance_id, container_id, retry_count, cost_usd, error,
			created_at, updated_at, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.Status, task.TaskMode, task.Harness, task.RepoURL, task.Branch, task.TargetBranch,
		task.ReviewPRNumber,
		task.Prompt, task.Context, task.Model, task.Effort,
		task.MaxBudgetUSD, task.MaxRuntimeMin, task.MaxTurns,
		boolToInt(task.CreatePR), boolToInt(task.SelfReview), task.PRTitle, task.PRBody, task.PRURL,
		task.AllowedToolsJSON(), task.ClaudeMD, task.EnvVarsJSON(),
		task.InstanceID, task.ContainerID, task.RetryCount, task.CostUSD, task.Error,
		task.CreatedAt.Format(time.RFC3339), task.UpdatedAt.Format(time.RFC3339),
		timePtr(task.StartedAt), timePtr(task.CompletedAt),
	)
	return err
}

func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*models.Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
		id, status, task_mode, harness, repo_url, branch, target_branch, review_pr_number,
		prompt, context,
		model, effort, max_budget_usd, max_runtime_min, max_turns,
		create_pr, self_review, pr_title, pr_body, pr_url,
		allowed_tools, claude_md, env_vars,
		instance_id, container_id, retry_count, cost_usd, error,
		created_at, updated_at, started_at, completed_at
		FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *SQLiteStore) ListTasks(ctx context.Context, filter TaskFilter) ([]*models.Task, error) {
	query := "SELECT id, status, task_mode, harness, repo_url, branch, target_branch, review_pr_number, prompt, context, model, effort, max_budget_usd, max_runtime_min, max_turns, create_pr, self_review, pr_title, pr_body, pr_url, allowed_tools, claude_md, env_vars, instance_id, container_id, retry_count, cost_usd, error, created_at, updated_at, started_at, completed_at FROM tasks"
	var args []any
	var where []string

	if filter.Status != nil {
		where = append(where, "status = ?")
		args = append(args, string(*filter.Status))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at ASC"
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*models.Task
	for rows.Next() {
		t, err := scanTaskRows(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *SQLiteStore) UpdateTask(ctx context.Context, task *models.Task) error {
	task.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET
			status=?, task_mode=?, harness=?, repo_url=?, branch=?, target_branch=?,
			review_pr_number=?, prompt=?, context=?,
			model=?, effort=?, max_budget_usd=?, max_runtime_min=?, max_turns=?,
			create_pr=?, self_review=?, pr_title=?, pr_body=?, pr_url=?,
			allowed_tools=?, claude_md=?, env_vars=?,
			instance_id=?, container_id=?, retry_count=?, cost_usd=?, error=?,
			updated_at=?, started_at=?, completed_at=?
		WHERE id = ?`,
		task.Status, task.TaskMode, task.Harness, task.RepoURL, task.Branch, task.TargetBranch,
		task.ReviewPRNumber, task.Prompt, task.Context, task.Model, task.Effort,
		task.MaxBudgetUSD, task.MaxRuntimeMin, task.MaxTurns,
		boolToInt(task.CreatePR), boolToInt(task.SelfReview), task.PRTitle, task.PRBody, task.PRURL,
		task.AllowedToolsJSON(), task.ClaudeMD, task.EnvVarsJSON(),
		task.InstanceID, task.ContainerID, task.RetryCount, task.CostUSD, task.Error,
		task.UpdatedAt.Format(time.RFC3339), timePtr(task.StartedAt), timePtr(task.CompletedAt),
		task.ID,
	)
	return err
}

func (s *SQLiteStore) DeleteTask(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id)
	return err
}

// --- Instances ---

func (s *SQLiteStore) CreateInstance(ctx context.Context, inst *models.Instance) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO instances (instance_id, instance_type, availability_zone, private_ip, status, max_containers, running_containers, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inst.InstanceID, inst.InstanceType, inst.AvailabilityZone, inst.PrivateIP,
		inst.Status, inst.MaxContainers, inst.RunningContainers,
		inst.CreatedAt.Format(time.RFC3339), inst.UpdatedAt.Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteStore) GetInstance(ctx context.Context, id string) (*models.Instance, error) {
	row := s.db.QueryRowContext(ctx, `SELECT instance_id, instance_type, availability_zone, private_ip, status, max_containers, running_containers, created_at, updated_at FROM instances WHERE instance_id = ?`, id)
	return scanInstance(row)
}

func (s *SQLiteStore) ListInstances(ctx context.Context, status *models.InstanceStatus) ([]*models.Instance, error) {
	query := "SELECT instance_id, instance_type, availability_zone, private_ip, status, max_containers, running_containers, created_at, updated_at FROM instances"
	var args []any
	if status != nil {
		query += " WHERE status = ?"
		args = append(args, string(*status))
	}
	query += " ORDER BY created_at ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []*models.Instance
	for rows.Next() {
		inst, err := scanInstanceRows(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}

func (s *SQLiteStore) UpdateInstance(ctx context.Context, inst *models.Instance) error {
	inst.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		UPDATE instances SET
			instance_type=?, availability_zone=?, private_ip=?, status=?,
			max_containers=?, running_containers=?, updated_at=?
		WHERE instance_id = ?`,
		inst.InstanceType, inst.AvailabilityZone, inst.PrivateIP, inst.Status,
		inst.MaxContainers, inst.RunningContainers, inst.UpdatedAt.Format(time.RFC3339),
		inst.InstanceID,
	)
	return err
}

// --- helpers ---

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (*models.Task, error) {
	var t models.Task
	var createPR, selfReview int
	var allowedToolsJSON, envVarsJSON string
	var createdAt, updatedAt string
	var startedAt, completedAt sql.NullString

	err := row.Scan(
		&t.ID, &t.Status, &t.TaskMode, &t.Harness, &t.RepoURL, &t.Branch, &t.TargetBranch,
		&t.ReviewPRNumber,
		&t.Prompt, &t.Context, &t.Model, &t.Effort,
		&t.MaxBudgetUSD, &t.MaxRuntimeMin, &t.MaxTurns,
		&createPR, &selfReview, &t.PRTitle, &t.PRBody, &t.PRURL,
		&allowedToolsJSON, &t.ClaudeMD, &envVarsJSON,
		&t.InstanceID, &t.ContainerID, &t.RetryCount, &t.CostUSD, &t.Error,
		&createdAt, &updatedAt, &startedAt, &completedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	t.CreatePR = createPR != 0
	t.SelfReview = selfReview != 0
	json.Unmarshal([]byte(allowedToolsJSON), &t.AllowedTools)
	json.Unmarshal([]byte(envVarsJSON), &t.EnvVars)
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if startedAt.Valid {
		ts, _ := time.Parse(time.RFC3339, startedAt.String)
		t.StartedAt = &ts
	}
	if completedAt.Valid {
		ts, _ := time.Parse(time.RFC3339, completedAt.String)
		t.CompletedAt = &ts
	}

	return &t, nil
}

func scanTaskRows(rows *sql.Rows) (*models.Task, error) {
	return scanTask(rows)
}

func scanInstance(row scanner) (*models.Instance, error) {
	var inst models.Instance
	var createdAt, updatedAt string

	err := row.Scan(
		&inst.InstanceID, &inst.InstanceType, &inst.AvailabilityZone,
		&inst.PrivateIP, &inst.Status, &inst.MaxContainers,
		&inst.RunningContainers, &createdAt, &updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	inst.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	inst.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &inst, nil
}

func scanInstanceRows(rows *sql.Rows) (*models.Instance, error) {
	return scanInstance(rows)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func timePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}
