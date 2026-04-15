package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" driver for database/sql
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
)

// querier abstracts over *pgxpool.Pool and pgx.Tx so that all query methods
// work identically in both transactional and non-transactional contexts.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PostgresStore implements Store using pgxpool.
type PostgresStore struct {
	pool *pgxpool.Pool
	q    querier
}

// NewPostgres connects to a Postgres database, runs goose migrations, and
// returns a ready-to-use PostgresStore.
func NewPostgres(ctx context.Context, databaseURL string, migrationsDir string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	// Run goose migrations using the pgx stdlib driver.
	gooseDB, err := goose.OpenDBWithDriver("pgx", databaseURL)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("goose open: %w", err)
	}
	defer gooseDB.Close()

	if err := goose.Up(gooseDB, migrationsDir); err != nil {
		pool.Close()
		return nil, fmt.Errorf("goose up: %w", err)
	}

	return &PostgresStore{pool: pool, q: pool}, nil
}

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

// --- Tasks ---

const taskColumns = `id, status, task_mode, harness, repo_url, branch, target_branch,
	prompt, context,
	model, effort, max_budget_usd, max_runtime_sec, max_turns,
	create_pr, self_review, save_agent_output, pr_title, pr_body, pr_url, output_url,
	allowed_tools, claude_md, env_vars,
	instance_id, container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
	ready_for_retry, reply_channel, agent_image,
	created_at, updated_at, started_at, completed_at`

func (s *PostgresStore) CreateTask(ctx context.Context, task *models.Task) error {
	allowedTools, _ := json.Marshal(task.AllowedTools)
	if task.AllowedTools == nil {
		allowedTools = []byte("[]")
	}
	envVars, _ := json.Marshal(task.EnvVars)
	if task.EnvVars == nil {
		envVars = []byte("{}")
	}

	_, err := s.q.Exec(ctx, `
		INSERT INTO tasks (
			id, status, task_mode, harness, repo_url, branch, target_branch,
			prompt, context,
			model, effort, max_budget_usd, max_runtime_sec, max_turns,
			create_pr, self_review, save_agent_output, pr_title, pr_body, pr_url, output_url,
			allowed_tools, claude_md, env_vars,
			instance_id, container_id, retry_count, user_retry_count, cost_usd, elapsed_time_sec, error,
			ready_for_retry, reply_channel, agent_image,
			created_at, updated_at, started_at, completed_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9,
			$10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19, $20, $21,
			$22, $23, $24,
			$25, $26, $27, $28, $29, $30, $31,
			$32, $33, $34,
			$35, $36, $37, $38
		)`,
		task.ID, task.Status, task.TaskMode, task.Harness, task.RepoURL, task.Branch, task.TargetBranch,
		task.Prompt, task.Context, task.Model, task.Effort,
		task.MaxBudgetUSD, task.MaxRuntimeSec, task.MaxTurns,
		task.CreatePR, task.SelfReview, task.SaveAgentOutput,
		task.PRTitle, task.PRBody, task.PRURL, task.OutputURL,
		allowedTools, task.ClaudeMD, envVars,
		task.InstanceID, task.ContainerID, task.RetryCount, task.UserRetryCount, task.CostUSD, task.ElapsedTimeSec, task.Error,
		task.ReadyForRetry, task.ReplyChannel, task.AgentImage,
		task.CreatedAt, task.UpdatedAt, task.StartedAt, task.CompletedAt,
	)
	return err
}

func (s *PostgresStore) GetTask(ctx context.Context, id string) (*models.Task, error) {
	row := s.q.QueryRow(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = $1`, id)
	return scanPGTask(row)
}

func (s *PostgresStore) ListTasks(ctx context.Context, filter TaskFilter) ([]*models.Task, error) {
	query := "SELECT " + taskColumns + " FROM tasks"
	var args []any
	var where []string
	argN := 1

	if filter.Status != nil {
		where = append(where, fmt.Sprintf("status = $%d", argN))
		args = append(args, string(*filter.Status))
		argN++
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

	rows, err := s.q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*models.Task
	for rows.Next() {
		t, err := scanPGTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *PostgresStore) DeleteTask(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx, "DELETE FROM tasks WHERE id = $1", id)
	return err
}

func (s *PostgresStore) UpdateTaskStatus(ctx context.Context, id string, status models.TaskStatus, taskErr string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE tasks SET status=$1, error=$2, updated_at=$3 WHERE id=$4",
		status, taskErr, time.Now().UTC(), id,
	)
	return err
}

func (s *PostgresStore) AssignTask(ctx context.Context, id string, instanceID string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE tasks SET status=$1, instance_id=$2, updated_at=$3 WHERE id=$4",
		models.TaskStatusProvisioning, instanceID, time.Now().UTC(), id,
	)
	return err
}

func (s *PostgresStore) StartTask(ctx context.Context, id string, containerID string) error {
	now := time.Now().UTC()
	_, err := s.q.Exec(ctx,
		"UPDATE tasks SET status=$1, container_id=$2, started_at=$3, updated_at=$4 WHERE id=$5",
		models.TaskStatusRunning, containerID, now, now, id,
	)
	return err
}

func (s *PostgresStore) CompleteTask(ctx context.Context, id string, result TaskResult) error {
	now := time.Now().UTC()
	_, err := s.q.Exec(ctx,
		`UPDATE tasks SET status=$1, error=$2, pr_url=$3, output_url=$4, cost_usd=$5, elapsed_time_sec=$6,
		 repo_url=COALESCE(NULLIF($7, ''), repo_url),
		 target_branch=COALESCE(NULLIF($8, ''), target_branch),
		 task_mode=COALESCE(NULLIF($9, ''), task_mode),
		 completed_at=$10, updated_at=$11 WHERE id=$12`,
		result.Status, result.Error, result.PRURL, result.OutputURL, result.CostUSD, result.ElapsedTimeSec,
		result.RepoURL, result.TargetBranch, result.TaskMode,
		now, now, id,
	)
	return err
}

func (s *PostgresStore) RequeueTask(ctx context.Context, id string, reason string) error {
	now := time.Now().UTC()
	_, err := s.q.Exec(ctx,
		`UPDATE tasks SET status=$1, instance_id='', container_id='', started_at=NULL,
		 retry_count=retry_count+1, ready_for_retry=false, error=$2, updated_at=$3 WHERE id=$4`,
		models.TaskStatusPending, "re-queued: "+reason+" at "+now.Format(time.RFC3339),
		now, id,
	)
	return err
}

func (s *PostgresStore) CancelTask(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := s.q.Exec(ctx,
		"UPDATE tasks SET status=$1, completed_at=$2, updated_at=$3 WHERE id=$4",
		models.TaskStatusCancelled, now, now, id,
	)
	return err
}

func (s *PostgresStore) ClearTaskAssignment(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE tasks SET instance_id='', container_id='', updated_at=$1 WHERE id=$2",
		time.Now().UTC(), id,
	)
	return err
}

func (s *PostgresStore) MarkReadyForRetry(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE tasks SET ready_for_retry=true, updated_at=$1 WHERE id=$2",
		time.Now().UTC(), id,
	)
	return err
}

// RetryTask atomically requeues a task for user-initiated retry. It checks
// ready_for_retry=true and user_retry_count < maxRetries in the WHERE clause
// to prevent retries before cleanup and double-retries. Returns ErrNotFound
// if no row matched (task not ready or cap reached).
func (s *PostgresStore) RetryTask(ctx context.Context, id string, maxRetries int) error {
	now := time.Now().UTC()
	tag, err := s.q.Exec(ctx,
		`UPDATE tasks SET status=$1, instance_id='', container_id='', started_at=NULL,
		 completed_at=NULL, retry_count=retry_count+1, user_retry_count=user_retry_count+1,
		 ready_for_retry=false, error=$2, updated_at=$3
		 WHERE id=$4 AND ready_for_retry=true AND user_retry_count < $5`,
		models.TaskStatusPending,
		"re-queued: user_retry at "+now.Format(time.RFC3339),
		now, id, maxRetries,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("task %s is not ready for retry", id)
	}
	return nil
}

// --- Instances ---

const instanceColumns = `instance_id, instance_type, availability_zone, private_ip, status, max_containers, running_containers, created_at, updated_at`

func (s *PostgresStore) CreateInstance(ctx context.Context, inst *models.Instance) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO instances (instance_id, instance_type, availability_zone, private_ip, status, max_containers, running_containers, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		inst.InstanceID, inst.InstanceType, inst.AvailabilityZone, inst.PrivateIP,
		inst.Status, inst.MaxContainers, inst.RunningContainers,
		inst.CreatedAt, inst.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GetInstance(ctx context.Context, id string) (*models.Instance, error) {
	row := s.q.QueryRow(ctx, `SELECT `+instanceColumns+` FROM instances WHERE instance_id = $1`, id)
	return scanPGInstance(row)
}

func (s *PostgresStore) ListInstances(ctx context.Context, status *models.InstanceStatus) ([]*models.Instance, error) {
	query := "SELECT " + instanceColumns + " FROM instances"
	var args []any
	if status != nil {
		query += " WHERE status = $1"
		args = append(args, string(*status))
	}
	query += " ORDER BY created_at ASC"

	rows, err := s.q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []*models.Instance
	for rows.Next() {
		inst, err := scanPGInstance(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, inst)
	}
	return instances, rows.Err()
}

func (s *PostgresStore) UpdateInstanceStatus(ctx context.Context, id string, status models.InstanceStatus) error {
	now := time.Now().UTC()
	var query string
	if status == models.InstanceStatusTerminated {
		query = "UPDATE instances SET status=$1, running_containers=0, updated_at=$2 WHERE instance_id=$3"
	} else {
		query = "UPDATE instances SET status=$1, updated_at=$2 WHERE instance_id=$3"
	}
	_, err := s.q.Exec(ctx, query, status, now, id)
	return err
}

func (s *PostgresStore) IncrementRunningContainers(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE instances SET running_containers=running_containers+1, updated_at=$1 WHERE instance_id=$2",
		time.Now().UTC(), id,
	)
	return err
}

func (s *PostgresStore) DecrementRunningContainers(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE instances SET running_containers=GREATEST(running_containers-1, 0), updated_at=$1 WHERE instance_id=$2",
		time.Now().UTC(), id,
	)
	return err
}

func (s *PostgresStore) UpdateInstanceDetails(ctx context.Context, id string, privateIP, az string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE instances SET private_ip=$1, availability_zone=$2, updated_at=$3 WHERE instance_id=$4",
		privateIP, az, time.Now().UTC(), id,
	)
	return err
}

func (s *PostgresStore) ResetRunningContainers(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE instances SET running_containers=0, updated_at=$1 WHERE instance_id=$2",
		time.Now().UTC(), id,
	)
	return err
}

// --- Allowed senders ---

func (s *PostgresStore) CreateAllowedSender(ctx context.Context, sender *models.AllowedSender) error {
	_, err := s.q.Exec(ctx,
		"INSERT INTO allowed_senders (channel_type, address, enabled, created_at) VALUES ($1, $2, $3, $4)",
		sender.ChannelType, sender.Address, sender.Enabled, sender.CreatedAt,
	)
	return err
}

func (s *PostgresStore) GetAllowedSender(ctx context.Context, channelType, address string) (*models.AllowedSender, error) {
	row := s.q.QueryRow(ctx,
		"SELECT channel_type, address, enabled, created_at FROM allowed_senders WHERE channel_type = $1 AND address = $2",
		channelType, address,
	)

	var sender models.AllowedSender
	err := row.Scan(&sender.ChannelType, &sender.Address, &sender.Enabled, &sender.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &sender, nil
}

// --- Discord installs ---

func (s *PostgresStore) UpsertDiscordInstall(ctx context.Context, install *models.DiscordInstall) error {
	roles := install.AllowedRoles
	if roles == nil {
		roles = []string{}
	}
	allowedRoles, err := json.Marshal(roles)
	if err != nil {
		return fmt.Errorf("marshal allowed_roles: %w", err)
	}
	_, err = s.q.Exec(ctx, `
		INSERT INTO discord_installs (guild_id, app_id, channel_id, allowed_roles, installed_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (guild_id) DO UPDATE SET
			app_id = EXCLUDED.app_id,
			channel_id = EXCLUDED.channel_id,
			allowed_roles = EXCLUDED.allowed_roles,
			updated_at = EXCLUDED.updated_at`,
		install.GuildID, install.AppID, install.ChannelID, allowedRoles,
		install.InstalledAt, install.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GetDiscordInstall(ctx context.Context, guildID string) (*models.DiscordInstall, error) {
	row := s.q.QueryRow(ctx,
		"SELECT guild_id, app_id, channel_id, allowed_roles, installed_at, updated_at FROM discord_installs WHERE guild_id = $1",
		guildID,
	)
	var install models.DiscordInstall
	var allowedRoles []byte
	err := row.Scan(&install.GuildID, &install.AppID, &install.ChannelID, &allowedRoles, &install.InstalledAt, &install.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := json.Unmarshal(allowedRoles, &install.AllowedRoles); err != nil {
		return nil, fmt.Errorf("unmarshal allowed_roles: %w", err)
	}
	return &install, nil
}

func (s *PostgresStore) DeleteDiscordInstall(ctx context.Context, guildID string) error {
	_, err := s.q.Exec(ctx, "DELETE FROM discord_installs WHERE guild_id = $1", guildID)
	return err
}

// --- Discord task threads ---

func (s *PostgresStore) UpsertDiscordTaskThread(ctx context.Context, thread *models.DiscordTaskThread) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO discord_task_threads (task_id, root_message_id, thread_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (task_id) DO UPDATE SET
			root_message_id = EXCLUDED.root_message_id,
			thread_id = EXCLUDED.thread_id,
			updated_at = EXCLUDED.updated_at`,
		thread.TaskID, thread.RootMessageID, thread.ThreadID, thread.CreatedAt, thread.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GetDiscordTaskThread(ctx context.Context, taskID string) (*models.DiscordTaskThread, error) {
	row := s.q.QueryRow(ctx,
		"SELECT task_id, root_message_id, thread_id, created_at, updated_at FROM discord_task_threads WHERE task_id = $1",
		taskID,
	)
	var thread models.DiscordTaskThread
	err := row.Scan(&thread.TaskID, &thread.RootMessageID, &thread.ThreadID, &thread.CreatedAt, &thread.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &thread, nil
}

// --- API keys ---

func (s *PostgresStore) HasAPIKeys(ctx context.Context) (bool, error) {
	var found bool
	err := s.q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM api_keys)").Scan(&found)
	return found, err
}

func (s *PostgresStore) GetAPIKeyByHash(ctx context.Context, keyHash string) (*models.APIKey, error) {
	row := s.q.QueryRow(ctx,
		"SELECT key_hash, name, permissions, expires_at, created_at, updated_at FROM api_keys WHERE key_hash = $1",
		keyHash,
	)

	var key models.APIKey
	var permissions []byte
	err := row.Scan(&key.KeyHash, &key.Name, &permissions, &key.ExpiresAt, &key.CreatedAt, &key.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(permissions) > 0 {
		if err := json.Unmarshal(permissions, &key.Permissions); err != nil {
			return nil, fmt.Errorf("unmarshal permissions: %w", err)
		}
	}
	return &key, nil
}

func (s *PostgresStore) CreateAPIKey(ctx context.Context, key *models.APIKey) error {
	perms := key.Permissions
	if perms == nil {
		perms = []string{}
	}
	permissions, err := json.Marshal(perms)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	_, err = s.q.Exec(ctx, `
		INSERT INTO api_keys (key_hash, name, permissions, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		key.KeyHash, key.Name, permissions, key.ExpiresAt, key.CreatedAt, key.UpdatedAt,
	)
	return err
}

// --- Transactions ---

func (s *PostgresStore) WithTx(ctx context.Context, fn func(Store) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	txStore := &PostgresStore{pool: s.pool, q: tx}
	if err := fn(txStore); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			log.Warn().Err(rbErr).Msg("tx rollback failed")
		}
		return err
	}
	return tx.Commit(ctx)
}

// --- Scan helpers ---

type pgScanner interface {
	Scan(dest ...any) error
}

func scanPGTask(row pgScanner) (*models.Task, error) {
	var t models.Task
	var allowedTools, envVars []byte

	err := row.Scan(
		&t.ID, &t.Status, &t.TaskMode, &t.Harness, &t.RepoURL, &t.Branch, &t.TargetBranch,
		&t.Prompt, &t.Context, &t.Model, &t.Effort,
		&t.MaxBudgetUSD, &t.MaxRuntimeSec, &t.MaxTurns,
		&t.CreatePR, &t.SelfReview, &t.SaveAgentOutput,
		&t.PRTitle, &t.PRBody, &t.PRURL, &t.OutputURL,
		&allowedTools, &t.ClaudeMD, &envVars,
		&t.InstanceID, &t.ContainerID, &t.RetryCount, &t.UserRetryCount, &t.CostUSD, &t.ElapsedTimeSec, &t.Error,
		&t.ReadyForRetry, &t.ReplyChannel, &t.AgentImage,
		&t.CreatedAt, &t.UpdatedAt, &t.StartedAt, &t.CompletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if len(allowedTools) > 0 {
		if err := json.Unmarshal(allowedTools, &t.AllowedTools); err != nil {
			return nil, fmt.Errorf("unmarshal allowed_tools: %w", err)
		}
	}
	if len(envVars) > 0 {
		if err := json.Unmarshal(envVars, &t.EnvVars); err != nil {
			return nil, fmt.Errorf("unmarshal env_vars: %w", err)
		}
	}

	return &t, nil
}

func scanPGInstance(row pgScanner) (*models.Instance, error) {
	var inst models.Instance

	err := row.Scan(
		&inst.InstanceID, &inst.InstanceType, &inst.AvailabilityZone,
		&inst.PrivateIP, &inst.Status, &inst.MaxContainers,
		&inst.RunningContainers, &inst.CreatedAt, &inst.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return &inst, nil
}

// --- Readings ---

// readingArgs prepares the common parameter list for reading insert queries.
func readingArgs(r *models.Reading) ([]any, error) {
	var connections []byte
	if len(r.Connections) == 0 {
		connections = []byte("[]")
	} else {
		var err error
		connections, err = json.Marshal(r.Connections)
		if err != nil {
			return nil, fmt.Errorf("marshal connections: %w", err)
		}
	}

	rawOutput := r.RawOutput
	if rawOutput == nil {
		rawOutput = []byte("{}")
	}

	var embedding *pgvector.Vector
	if len(r.Embedding) > 0 {
		v := pgvector.NewVector(r.Embedding)
		embedding = &v
	}

	return []any{
		r.ID, r.TaskID, r.URL, r.Title, r.TLDR,
		r.Tags, r.Keywords, r.People, r.Orgs,
		r.NoveltyVerdict, connections, r.Summary, rawOutput,
		embedding, r.CreatedAt,
	}, nil
}

const readingInsertCols = `
		INSERT INTO readings (
			id, task_id, url, title, tldr,
			tags, keywords, people, orgs,
			novelty_verdict, connections, summary, raw_output,
			embedding, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			$14, $15
		)`

func (s *PostgresStore) CreateReading(ctx context.Context, r *models.Reading) error {
	args, err := readingArgs(r)
	if err != nil {
		return err
	}
	_, err = s.q.Exec(ctx, readingInsertCols, args...)
	return err
}

func (s *PostgresStore) UpsertReading(ctx context.Context, r *models.Reading) error {
	args, err := readingArgs(r)
	if err != nil {
		return err
	}
	_, err = s.q.Exec(ctx, readingInsertCols+`
		ON CONFLICT (url) DO UPDATE SET
			task_id         = EXCLUDED.task_id,
			title           = EXCLUDED.title,
			tldr            = EXCLUDED.tldr,
			tags            = EXCLUDED.tags,
			keywords        = EXCLUDED.keywords,
			people          = EXCLUDED.people,
			orgs            = EXCLUDED.orgs,
			novelty_verdict = EXCLUDED.novelty_verdict,
			connections     = EXCLUDED.connections,
			summary         = EXCLUDED.summary,
			raw_output      = EXCLUDED.raw_output,
			embedding       = EXCLUDED.embedding`, args...)
	return err
}
