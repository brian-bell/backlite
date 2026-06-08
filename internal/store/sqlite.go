package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"
	_ "modernc.org/sqlite"

	"github.com/brian-bell/backlite/internal/models"
)

type sqliteQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SQLiteStore implements Store using a local SQLite database.
type SQLiteStore struct {
	db *sql.DB
	q  sqliteQuerier
}

// NewSQLite opens a local SQLite database, runs goose migrations, and returns
// a ready-to-use store.
func NewSQLite(ctx context.Context, databasePath string, migrationsDir string) (*SQLiteStore, error) {
	if databasePath == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	}
	for _, pragma := range pragmas {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite database: %w", err)
	}

	if err := goose.SetDialect("sqlite3"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set goose dialect: %w", err)
	}
	if err := goose.Up(db, migrationsDir); err != nil {
		db.Close()
		return nil, fmt.Errorf("goose up: %w", err)
	}

	return &SQLiteStore{db: db, q: db}, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

const taskColumns = `id, status, task_mode, harness, repo_url, branch, target_branch,
	prompt, context,
	model, effort, max_budget_usd, max_runtime_sec, max_turns,
	create_pr, self_review, save_agent_output, pr_title, pr_body, pr_url, output_url,
	allowed_tools, claude_md, env_vars,
	container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
	ready_for_retry, agent_image, parent_task_id,
	created_at, updated_at, started_at, completed_at`

func (s *SQLiteStore) CreateTask(ctx context.Context, task *models.Task) error {
	allowedTools, err := marshalJSONSlice(task.AllowedTools)
	if err != nil {
		return err
	}
	envVars, err := marshalJSONMap(task.EnvVars)
	if err != nil {
		return err
	}

	_, err = s.q.ExecContext(ctx, `
		INSERT INTO tasks (
			id, status, task_mode, harness, repo_url, branch, target_branch,
			prompt, context,
			model, effort, max_budget_usd, max_runtime_sec, max_turns,
			create_pr, self_review, save_agent_output, pr_title, pr_body, pr_url, output_url,
			allowed_tools, claude_md, env_vars,
			container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
			ready_for_retry, agent_image, parent_task_id,
			created_at, updated_at, started_at, completed_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?
		)`,
		task.ID, task.Status, task.TaskMode, task.Harness, task.RepoURL, task.Branch, task.TargetBranch,
		task.Prompt, task.Context, task.Model, task.Effort,
		task.MaxBudgetUSD, task.MaxRuntimeSec, task.MaxTurns,
		task.CreatePR, task.SelfReview, task.SaveAgentOutput,
		task.PRTitle, task.PRBody, task.PRURL, task.OutputURL,
		allowedTools, task.ClaudeMD, envVars,
		task.ContainerID, task.RetryCount, task.UserRetryCount, task.CostUSD, task.ElapsedTimeSec, task.Error,
		task.ReadyForRetry, task.AgentImage, nullableString(task.ParentTaskID),
		timeString(task.CreatedAt), timeString(task.UpdatedAt), nullableTimeString(task.StartedAt), nullableTimeString(task.CompletedAt),
	)
	return err
}

func (s *SQLiteStore) GetTask(ctx context.Context, id string) (*models.Task, error) {
	row := s.q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	return scanTask(row)
}

func (s *SQLiteStore) ListTasks(ctx context.Context, filter TaskFilter) ([]*models.Task, error) {
	query := "SELECT " + taskColumns + " FROM tasks"
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

	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*models.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *SQLiteStore) DeleteTask(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx, "DELETE FROM tasks WHERE id = ?", id)
	return err
}

func (s *SQLiteStore) UpdateTaskStatus(ctx context.Context, id string, status models.TaskStatus, taskErr string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET status=?, error=?, updated_at=? WHERE id=?",
		status, taskErr, timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) AssignTask(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET status=?, updated_at=? WHERE id=?",
		models.TaskStatusProvisioning, timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) StartTask(ctx context.Context, id string, containerID string, agentImage string) error {
	now := timeString(time.Now().UTC())
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET status=?, container_id=?, agent_image=?, started_at=?, updated_at=? WHERE id=?",
		models.TaskStatusRunning, containerID, agentImage, now, now, id,
	)
	return err
}

func (s *SQLiteStore) CompleteTask(ctx context.Context, id string, result TaskResult) error {
	now := time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `UPDATE tasks SET
			status=?,
			error=?,
			pr_url=?,
			output_url=?,
			cost_usd=?,
			elapsed_time_sec=?,
			repo_url=COALESCE(NULLIF(?, ''), repo_url),
			target_branch=COALESCE(NULLIF(?, ''), target_branch),
			task_mode=COALESCE(NULLIF(?, ''), task_mode),
			completed_at=?,
			updated_at=?
		WHERE id=?`,
		result.Status, result.Error, result.PRURL, result.OutputURL, result.CostUSD, result.ElapsedTimeSec,
		result.RepoURL, result.TargetBranch, result.TaskMode,
		timeString(now), timeString(now), id,
	)
	return err
}

func (s *SQLiteStore) RequeueTask(ctx context.Context, id string, reason string) error {
	now := time.Now().UTC()
	_, err := s.q.ExecContext(ctx, `UPDATE tasks SET
			status=?,
			container_id='',
			started_at=NULL,
			retry_count=retry_count+1,
			ready_for_retry=false,
			error=?,
			output_url='',
			updated_at=?
		WHERE id=?`,
		models.TaskStatusPending, "re-queued: "+reason+" at "+now.Format(time.RFC3339), timeString(now), id,
	)
	return err
}

func (s *SQLiteStore) CancelTask(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET status=?, completed_at=?, updated_at=? WHERE id=?",
		models.TaskStatusCancelled, timeString(now), timeString(now), id,
	)
	return err
}

func (s *SQLiteStore) ClearTaskAssignment(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET container_id='', updated_at=? WHERE id=?",
		timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) MarkReadyForRetry(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET ready_for_retry=true, updated_at=? WHERE id=?",
		timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) RetryTask(ctx context.Context, id string, maxRetries int) error {
	now := time.Now().UTC()
	result, err := s.q.ExecContext(ctx, `UPDATE tasks SET
			status=?,
			container_id='',
			started_at=NULL,
			completed_at=NULL,
			retry_count=retry_count+1,
			user_retry_count=user_retry_count+1,
			ready_for_retry=false,
			error=?,
			output_url='',
			updated_at=?
		WHERE id=? AND ready_for_retry=true AND user_retry_count < ?`,
		models.TaskStatusPending,
		"re-queued: user_retry at "+now.Format(time.RFC3339),
		timeString(now), id, maxRetries,
	)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("task %s is not ready for retry", id)
	}
	return nil
}

func (s *SQLiteStore) HasAPIKeys(ctx context.Context) (bool, error) {
	var found bool
	err := s.q.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM api_keys)").Scan(&found)
	return found, err
}

func (s *SQLiteStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*models.APIKey, error) {
	row := s.q.QueryRowContext(ctx,
		"SELECT key_hash, name, permissions, expires_at, created_at, updated_at FROM api_keys WHERE key_hash = ?",
		keyHash,
	)

	var (
		key             models.APIKey
		permissionsJSON string
		expiresAt       sql.NullString
		createdAt       string
		updatedAt       string
	)
	err := row.Scan(&key.KeyHash, &key.Name, &permissionsJSON, &expiresAt, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if permissionsJSON != "" {
		if err := json.Unmarshal([]byte(permissionsJSON), &key.Permissions); err != nil {
			return nil, fmt.Errorf("unmarshal permissions: %w", err)
		}
	}
	if expiresAt.Valid {
		parsed, err := parseTime(expiresAt.String)
		if err != nil {
			return nil, err
		}
		key.ExpiresAt = &parsed
	}
	if key.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if key.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &key, nil
}

func (s *SQLiteStore) CreateAPIKey(ctx context.Context, key *models.APIKey) error {
	perms := key.Permissions
	if perms == nil {
		perms = []string{}
	}
	permissions, err := json.Marshal(perms)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	_, err = s.q.ExecContext(ctx, `
		INSERT INTO api_keys (key_hash, name, permissions, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		key.KeyHash, key.Name, string(permissions), nullableTimeString(key.ExpiresAt), timeString(key.CreatedAt), timeString(key.UpdatedAt),
	)
	return err
}

func (s *SQLiteStore) WithTx(ctx context.Context, fn func(Store) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txStore := &SQLiteStore{db: s.db, q: tx}
	if err := fn(txStore); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			log.Warn().Err(rbErr).Msg("tx rollback failed")
		}
		return err
	}
	return tx.Commit()
}

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanTask(row sqlScanner) (*models.Task, error) {
	var (
		t                models.Task
		allowedToolsJSON string
		envVarsJSON      string
		parentTaskID     sql.NullString
		createdAt        string
		updatedAt        string
		startedAt        sql.NullString
		completedAt      sql.NullString
	)

	err := row.Scan(
		&t.ID, &t.Status, &t.TaskMode, &t.Harness, &t.RepoURL, &t.Branch, &t.TargetBranch,
		&t.Prompt, &t.Context, &t.Model, &t.Effort,
		&t.MaxBudgetUSD, &t.MaxRuntimeSec, &t.MaxTurns,
		&t.CreatePR, &t.SelfReview, &t.SaveAgentOutput,
		&t.PRTitle, &t.PRBody, &t.PRURL, &t.OutputURL,
		&allowedToolsJSON, &t.ClaudeMD, &envVarsJSON,
		&t.ContainerID, &t.RetryCount, &t.UserRetryCount, &t.CostUSD, &t.ElapsedTimeSec, &t.Error,
		&t.ReadyForRetry, &t.AgentImage, &parentTaskID,
		&createdAt, &updatedAt, &startedAt, &completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if parentTaskID.Valid {
		v := parentTaskID.String
		t.ParentTaskID = &v
	}

	if err := unmarshalJSONString(allowedToolsJSON, &t.AllowedTools); err != nil {
		return nil, fmt.Errorf("unmarshal allowed_tools: %w", err)
	}
	if err := unmarshalJSONString(envVarsJSON, &t.EnvVars); err != nil {
		return nil, fmt.Errorf("unmarshal env_vars: %w", err)
	}
	if t.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	if startedAt.Valid {
		parsed, err := parseTime(startedAt.String)
		if err != nil {
			return nil, err
		}
		t.StartedAt = &parsed
	}
	if completedAt.Valid {
		parsed, err := parseTime(completedAt.String)
		if err != nil {
			return nil, err
		}
		t.CompletedAt = &parsed
	}

	return &t, nil
}

func timeString(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableTimeString(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timeString(*t)
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func parseTime(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", raw, err)
	}
	return parsed.UTC(), nil
}

func marshalJSONSlice(v []string) (string, error) {
	if v == nil {
		return "[]", nil
	}
	return jsonString(v)
}

func marshalJSONMap(v map[string]string) (string, error) {
	if v == nil {
		return "{}", nil
	}
	return jsonString(v)
}

func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalJSONString(raw string, dest any) error {
	if raw == "" {
		raw = "null"
	}
	return json.Unmarshal([]byte(raw), dest)
}
