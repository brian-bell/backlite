package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"
	_ "modernc.org/sqlite"

	"github.com/backflow-labs/backflow/internal/models"
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

// PostgresStore is kept as a compatibility alias for older tests and callers.
type PostgresStore = SQLiteStore

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

// NewPostgres is kept as a compatibility wrapper for older tests and callers.
func NewPostgres(ctx context.Context, databasePath string, migrationsDir string) (*SQLiteStore, error) {
	return NewSQLite(ctx, databasePath, migrationsDir)
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

const taskColumns = `id, status, task_mode, harness, repo_url, branch, target_branch,
	prompt, context,
	model, effort, max_budget_usd, max_runtime_sec, max_turns,
	create_pr, self_review, save_agent_output, pr_title, pr_body, pr_url, output_url,
	allowed_tools, claude_md, env_vars,
	instance_id, container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
	ready_for_retry, reply_channel, agent_image, force,
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
			instance_id, container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
			ready_for_retry, reply_channel, agent_image, force,
			created_at, updated_at, started_at, completed_at
		) VALUES (
			?, ?, ?, ?, ?, ?, ?,
			?, ?,
			?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?,
			?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?
		)`,
		task.ID, task.Status, task.TaskMode, task.Harness, task.RepoURL, task.Branch, task.TargetBranch,
		task.Prompt, task.Context, task.Model, task.Effort,
		task.MaxBudgetUSD, task.MaxRuntimeSec, task.MaxTurns,
		task.CreatePR, task.SelfReview, task.SaveAgentOutput,
		task.PRTitle, task.PRBody, task.PRURL, task.OutputURL,
		allowedTools, task.ClaudeMD, envVars,
		task.InstanceID, task.ContainerID, task.RetryCount, task.UserRetryCount, task.CostUSD, task.ElapsedTimeSec, task.Error,
		task.ReadyForRetry, task.ReplyChannel, task.AgentImage, task.Force,
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

func (s *SQLiteStore) AssignTask(ctx context.Context, id string, instanceID string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET status=?, instance_id=?, updated_at=? WHERE id=?",
		models.TaskStatusProvisioning, instanceID, timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) StartTask(ctx context.Context, id string, containerID string) error {
	now := time.Now().UTC()
	_, err := s.q.ExecContext(ctx,
		"UPDATE tasks SET status=?, container_id=?, started_at=?, updated_at=? WHERE id=?",
		models.TaskStatusRunning, containerID, timeString(now), timeString(now), id,
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
			instance_id='',
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
		"UPDATE tasks SET instance_id='', container_id='', updated_at=? WHERE id=?",
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
			instance_id='',
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

const instanceColumns = `instance_id, instance_type, availability_zone, private_ip, status, max_containers, running_containers, created_at, updated_at`

func (s *SQLiteStore) CreateInstance(ctx context.Context, inst *models.Instance) error {
	_, err := s.q.ExecContext(ctx, `
		INSERT INTO instances (instance_id, instance_type, availability_zone, private_ip, status, max_containers, running_containers, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inst.InstanceID, inst.InstanceType, inst.AvailabilityZone, inst.PrivateIP,
		inst.Status, inst.MaxContainers, inst.RunningContainers,
		timeString(inst.CreatedAt), timeString(inst.UpdatedAt),
	)
	return err
}

func (s *SQLiteStore) GetInstance(ctx context.Context, id string) (*models.Instance, error) {
	row := s.q.QueryRowContext(ctx, `SELECT `+instanceColumns+` FROM instances WHERE instance_id = ?`, id)
	return scanInstance(row)
}

func (s *SQLiteStore) ListInstances(ctx context.Context, status *models.InstanceStatus) ([]*models.Instance, error) {
	query := "SELECT " + instanceColumns + " FROM instances"
	var args []any
	if status != nil {
		query += " WHERE status = ?"
		args = append(args, string(*status))
	}
	query += " ORDER BY created_at ASC"

	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []*models.Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}

func (s *SQLiteStore) UpdateInstanceStatus(ctx context.Context, id string, status models.InstanceStatus) error {
	now := time.Now().UTC()
	var (
		query string
		args  []any
	)
	if status == models.InstanceStatusTerminated {
		query = "UPDATE instances SET status=?, running_containers=0, updated_at=? WHERE instance_id=?"
		args = []any{status, timeString(now), id}
	} else {
		query = "UPDATE instances SET status=?, updated_at=? WHERE instance_id=?"
		args = []any{status, timeString(now), id}
	}
	_, err := s.q.ExecContext(ctx, query, args...)
	return err
}

func (s *SQLiteStore) IncrementRunningContainers(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE instances SET running_containers=running_containers+1, updated_at=? WHERE instance_id=?",
		timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) DecrementRunningContainers(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE instances SET running_containers=max(running_containers-1, 0), updated_at=? WHERE instance_id=?",
		timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) UpdateInstanceDetails(ctx context.Context, id string, privateIP, az string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE instances SET private_ip=?, availability_zone=?, updated_at=? WHERE instance_id=?",
		privateIP, az, timeString(time.Now().UTC()), id,
	)
	return err
}

func (s *SQLiteStore) ResetRunningContainers(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx,
		"UPDATE instances SET running_containers=0, updated_at=? WHERE instance_id=?",
		timeString(time.Now().UTC()), id,
	)
	return err
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

func (s *SQLiteStore) UpsertReading(ctx context.Context, r *models.Reading) error {
	args, err := readingArgs(r)
	if err != nil {
		return err
	}
	_, err = s.q.ExecContext(ctx, `
		INSERT INTO readings (
			id, task_id, url, title, tldr,
			tags, keywords, people, orgs,
			novelty_verdict, connections, summary, raw_output,
			embedding, is_available, created_at
		) VALUES (
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?
		)
		ON CONFLICT(url) DO UPDATE SET
			task_id         = excluded.task_id,
			title           = excluded.title,
			tldr            = excluded.tldr,
			tags            = excluded.tags,
			keywords        = excluded.keywords,
			people          = excluded.people,
			orgs            = excluded.orgs,
			novelty_verdict = excluded.novelty_verdict,
			connections     = excluded.connections,
			summary         = excluded.summary,
			raw_output      = excluded.raw_output,
			embedding       = excluded.embedding,
			is_available    = excluded.is_available`, args...)
	return err
}

func (s *SQLiteStore) GetReadingByURL(ctx context.Context, url string) (*models.Reading, error) {
	row := s.q.QueryRowContext(ctx, `
		SELECT id, task_id, url, title, tldr,
		       tags, keywords, people, orgs,
		       novelty_verdict, connections, summary, raw_output,
		       created_at
		FROM readings
		WHERE url = ?`, url)

	var (
		r               models.Reading
		tagsJSON        string
		keywordsJSON    string
		peopleJSON      string
		orgsJSON        string
		connectionsJSON string
		rawOutputJSON   string
		createdAt       string
	)
	err := row.Scan(
		&r.ID, &r.TaskID, &r.URL, &r.Title, &r.TLDR,
		&tagsJSON, &keywordsJSON, &peopleJSON, &orgsJSON,
		&r.NoveltyVerdict, &connectionsJSON, &r.Summary, &rawOutputJSON,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := unmarshalJSONString(tagsJSON, &r.Tags); err != nil {
		return nil, fmt.Errorf("unmarshal tags: %w", err)
	}
	if err := unmarshalJSONString(keywordsJSON, &r.Keywords); err != nil {
		return nil, fmt.Errorf("unmarshal keywords: %w", err)
	}
	if err := unmarshalJSONString(peopleJSON, &r.People); err != nil {
		return nil, fmt.Errorf("unmarshal people: %w", err)
	}
	if err := unmarshalJSONString(orgsJSON, &r.Orgs); err != nil {
		return nil, fmt.Errorf("unmarshal orgs: %w", err)
	}
	if err := unmarshalJSONString(connectionsJSON, &r.Connections); err != nil {
		return nil, fmt.Errorf("unmarshal connections: %w", err)
	}
	if rawOutputJSON != "" {
		r.RawOutput = json.RawMessage(rawOutputJSON)
	}
	if r.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *SQLiteStore) FindSimilarReadings(ctx context.Context, queryEmbedding []float32, limit int) ([]ReadingMatch, error) {
	if len(queryEmbedding) == 0 || limit <= 0 {
		return []ReadingMatch{}, nil
	}

	rows, err := s.q.QueryContext(ctx, `
		SELECT id, title, tldr, url, embedding
		FROM readings
		WHERE is_available = true
		  AND embedding IS NOT NULL
		  AND embedding != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []ReadingMatch
	for rows.Next() {
		var (
			match         ReadingMatch
			embeddingJSON string
		)
		if err := rows.Scan(&match.ID, &match.Title, &match.TLDR, &match.URL, &embeddingJSON); err != nil {
			return nil, err
		}
		var embedding []float32
		if err := unmarshalJSONString(embeddingJSON, &embedding); err != nil {
			return nil, fmt.Errorf("unmarshal embedding for %s: %w", match.ID, err)
		}
		match.Similarity = cosineSimilarity(queryEmbedding, embedding)
		if !math.IsNaN(match.Similarity) {
			matches = append(matches, match)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Similarity == matches[j].Similarity {
			return matches[i].ID < matches[j].ID
		}
		return matches[i].Similarity > matches[j].Similarity
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanTask(row sqlScanner) (*models.Task, error) {
	var (
		t                models.Task
		allowedToolsJSON string
		envVarsJSON      string
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
		&t.InstanceID, &t.ContainerID, &t.RetryCount, &t.UserRetryCount, &t.CostUSD, &t.ElapsedTimeSec, &t.Error,
		&t.ReadyForRetry, &t.ReplyChannel, &t.AgentImage, &t.Force,
		&createdAt, &updatedAt, &startedAt, &completedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
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

func scanInstance(row sqlScanner) (*models.Instance, error) {
	var (
		inst      models.Instance
		createdAt string
		updatedAt string
		err       error
	)

	err = row.Scan(
		&inst.InstanceID, &inst.InstanceType, &inst.AvailabilityZone,
		&inst.PrivateIP, &inst.Status, &inst.MaxContainers,
		&inst.RunningContainers, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if inst.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if inst.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &inst, nil
}

func readingArgs(r *models.Reading) ([]any, error) {
	tagsJSON, err := jsonString(r.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}
	keywordsJSON, err := jsonString(r.Keywords)
	if err != nil {
		return nil, fmt.Errorf("marshal keywords: %w", err)
	}
	peopleJSON, err := jsonString(r.People)
	if err != nil {
		return nil, fmt.Errorf("marshal people: %w", err)
	}
	orgsJSON, err := jsonString(r.Orgs)
	if err != nil {
		return nil, fmt.Errorf("marshal orgs: %w", err)
	}
	connectionsJSON, err := jsonString(r.Connections)
	if err != nil {
		return nil, fmt.Errorf("marshal connections: %w", err)
	}
	rawOutput := r.RawOutput
	if rawOutput == nil {
		rawOutput = []byte("{}")
	}
	embeddingJSON, err := jsonString(r.Embedding)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding: %w", err)
	}

	return []any{
		r.ID, r.TaskID, r.URL, r.Title, r.TLDR,
		tagsJSON, keywordsJSON, peopleJSON, orgsJSON,
		r.NoveltyVerdict, connectionsJSON, r.Summary, string(rawOutput),
		embeddingJSON, true, timeString(r.CreatedAt),
	}, nil
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

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return math.NaN()
	}
	var dot, magA, magB float64
	for i := range a {
		af := float64(a[i])
		bf := float64(b[i])
		dot += af * bf
		magA += af * af
		magB += bf * bf
	}
	if magA == 0 || magB == 0 {
		return math.NaN()
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}
