//go:build !nocontainers

package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	_ "modernc.org/sqlite"
)

var sharedPgConnStr string

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx, "pgvector/pgvector:pg16",
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

	sharedPgConnStr, err = pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Fatalf("get connection string: %v", err)
	}

	// Run goose migrations to create the Postgres schema.
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	if err := runGooseMigrations(sharedPgConnStr, migrationsDir); err != nil {
		log.Fatalf("goose migrations: %v", err)
	}

	code := m.Run()
	pgContainer.Terminate(ctx)
	os.Exit(code)
}

// newTestSQLiteDB creates a temporary SQLite database with the legacy schema.
func newTestSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(tmpDir, "backflow.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Create legacy SQLite schema — types differ from Postgres.
	_, err = db.Exec(`
		CREATE TABLE tasks (
			id               TEXT PRIMARY KEY,
			status           TEXT NOT NULL DEFAULT 'pending',
			task_mode        TEXT NOT NULL DEFAULT 'code',
			harness          TEXT NOT NULL DEFAULT 'claude_code',
			repo_url         TEXT NOT NULL,
			branch           TEXT NOT NULL DEFAULT '',
			target_branch    TEXT NOT NULL DEFAULT '',
			prompt           TEXT NOT NULL,
			context          TEXT NOT NULL DEFAULT '',
			model            TEXT NOT NULL DEFAULT '',
			effort           TEXT NOT NULL DEFAULT '',
			max_budget_usd   REAL NOT NULL DEFAULT 0,
			max_runtime_sec  INTEGER NOT NULL DEFAULT 0,
			max_turns        INTEGER NOT NULL DEFAULT 0,
			create_pr        INTEGER NOT NULL DEFAULT 0,
			self_review      INTEGER NOT NULL DEFAULT 0,
			save_agent_output INTEGER NOT NULL DEFAULT 1,
			pr_title         TEXT NOT NULL DEFAULT '',
			pr_body          TEXT NOT NULL DEFAULT '',
			pr_url           TEXT NOT NULL DEFAULT '',
			output_url       TEXT NOT NULL DEFAULT '',
			allowed_tools    TEXT NOT NULL DEFAULT '[]',
			claude_md        TEXT NOT NULL DEFAULT '',
			env_vars         TEXT NOT NULL DEFAULT '{}',
			instance_id      TEXT NOT NULL DEFAULT '',
			container_id     TEXT NOT NULL DEFAULT '',
			retry_count      INTEGER NOT NULL DEFAULT 0,
			cost_usd         REAL NOT NULL DEFAULT 0,
			elapsed_time_sec INTEGER NOT NULL DEFAULT 0,
			error            TEXT NOT NULL DEFAULT '',
			reply_channel    TEXT NOT NULL DEFAULT '',
			created_at       TEXT NOT NULL,
			updated_at       TEXT NOT NULL,
			started_at       TEXT,
			completed_at     TEXT
		);

		CREATE TABLE instances (
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

	`)
	if err != nil {
		t.Fatalf("create sqlite schema: %v", err)
	}
	return db
}

// newTestPgPool creates a fresh pgxpool and truncates all tables.
func newTestPgPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, sharedPgConnStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	if _, err := pool.Exec(ctx, "TRUNCATE tasks, instances, api_keys CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

func TestMigrate_Tasks(t *testing.T) {
	ctx := context.Background()
	sqliteDB := newTestSQLiteDB(t)
	pgPool := newTestPgPool(t)

	// Seed SQLite with a task exercising all type transforms.
	createdAt := "2025-01-15T10:30:00Z"
	updatedAt := "2025-01-15T11:00:00Z"
	startedAt := "2025-01-15T10:31:00Z"

	_, err := sqliteDB.Exec(`INSERT INTO tasks (
		id, status, task_mode, harness, repo_url, branch, target_branch,
		prompt, model, max_budget_usd, max_turns,
		create_pr, self_review, save_agent_output,
		allowed_tools, env_vars, cost_usd,
		instance_id, container_id, retry_count, elapsed_time_sec,
		created_at, updated_at, started_at, completed_at
	) VALUES (
		'bf_MIG001', 'completed', 'code', 'claude_code',
		'https://github.com/test/repo', 'backflow/test', 'main',
		'Fix the bug', 'claude-sonnet-4-6', 10.5, 200,
		1, 0, 1,
		'["Read","Write"]', '{"FOO":"bar"}', 1.23,
		'i-abc123', 'container-1', 2, 120,
		?, ?, ?, NULL
	)`, createdAt, updatedAt, startedAt)
	if err != nil {
		t.Fatalf("seed sqlite task: %v", err)
	}

	// Run migration.
	if err := migrate(ctx, sqliteDB, pgPool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Verify in Postgres.
	var (
		id, status, taskMode, harness, repoURL, branch, targetBranch string
		prompt, model, instanceID, containerID                       string
		maxBudgetUSD, costUSD                                        float64
		maxTurns, retryCount, elapsedTimeSec                         int
		createPR, selfReview, saveAgentOutput                        bool
		allowedToolsJSON, envVarsJSON                                string
		pgCreatedAt, pgUpdatedAt                                     time.Time
		pgStartedAt                                                  *time.Time
		pgCompletedAt                                                *time.Time
	)

	err = pgPool.QueryRow(ctx, `SELECT
		id, status, task_mode, harness, repo_url, branch, target_branch,
		prompt, model, max_budget_usd, max_turns,
		create_pr, self_review, save_agent_output,
		allowed_tools::text, env_vars::text, cost_usd,
		instance_id, container_id, retry_count, elapsed_time_sec,
		created_at, updated_at, started_at, completed_at
	FROM tasks WHERE id = 'bf_MIG001'`).Scan(
		&id, &status, &taskMode, &harness, &repoURL, &branch, &targetBranch,
		&prompt, &model, &maxBudgetUSD, &maxTurns,
		&createPR, &selfReview, &saveAgentOutput,
		&allowedToolsJSON, &envVarsJSON, &costUSD,
		&instanceID, &containerID, &retryCount, &elapsedTimeSec,
		&pgCreatedAt, &pgUpdatedAt, &pgStartedAt, &pgCompletedAt,
	)
	if err != nil {
		t.Fatalf("query postgres: %v", err)
	}

	// Text fields
	if id != "bf_MIG001" {
		t.Errorf("id = %q", id)
	}
	if status != "completed" {
		t.Errorf("status = %q", status)
	}
	if repoURL != "https://github.com/test/repo" {
		t.Errorf("repo_url = %q", repoURL)
	}
	if prompt != "Fix the bug" {
		t.Errorf("prompt = %q", prompt)
	}

	// Boolean transforms (INTEGER 0/1 → BOOLEAN)
	if !createPR {
		t.Error("create_pr should be true (was 1)")
	}
	if selfReview {
		t.Error("self_review should be false (was 0)")
	}
	if !saveAgentOutput {
		t.Error("save_agent_output should be true (was 1)")
	}

	// Float transforms (REAL → DOUBLE PRECISION)
	if maxBudgetUSD != 10.5 {
		t.Errorf("max_budget_usd = %f, want 10.5", maxBudgetUSD)
	}
	if costUSD != 1.23 {
		t.Errorf("cost_usd = %f, want 1.23", costUSD)
	}

	// Timestamp transforms (TEXT RFC3339 → TIMESTAMPTZ)
	wantCreated, _ := time.Parse(time.RFC3339, createdAt)
	if !pgCreatedAt.Equal(wantCreated) {
		t.Errorf("created_at = %v, want %v", pgCreatedAt, wantCreated)
	}
	wantUpdated, _ := time.Parse(time.RFC3339, updatedAt)
	if !pgUpdatedAt.Equal(wantUpdated) {
		t.Errorf("updated_at = %v, want %v", pgUpdatedAt, wantUpdated)
	}

	// Nullable timestamp — started_at should be set, completed_at should be NULL
	if pgStartedAt == nil {
		t.Fatal("started_at should not be nil")
	}
	wantStarted, _ := time.Parse(time.RFC3339, startedAt)
	if !pgStartedAt.Equal(wantStarted) {
		t.Errorf("started_at = %v, want %v", *pgStartedAt, wantStarted)
	}
	if pgCompletedAt != nil {
		t.Errorf("completed_at should be nil, got %v", *pgCompletedAt)
	}

	// Integer fields
	if maxTurns != 200 {
		t.Errorf("max_turns = %d, want 200", maxTurns)
	}
	if retryCount != 2 {
		t.Errorf("retry_count = %d, want 2", retryCount)
	}
	if elapsedTimeSec != 120 {
		t.Errorf("elapsed_time_sec = %d, want 120", elapsedTimeSec)
	}
}

func TestMigrate_Instances(t *testing.T) {
	ctx := context.Background()
	sqliteDB := newTestSQLiteDB(t)
	pgPool := newTestPgPool(t)

	createdAt := "2025-02-01T08:00:00Z"
	updatedAt := "2025-02-01T09:30:00Z"

	_, err := sqliteDB.Exec(`INSERT INTO instances (
		instance_id, instance_type, availability_zone, private_ip,
		status, max_containers, running_containers,
		created_at, updated_at
	) VALUES ('i-mig001', 'm7g.xlarge', 'us-east-1a', '10.0.1.5', 'running', 4, 2, ?, ?)`,
		createdAt, updatedAt)
	if err != nil {
		t.Fatalf("seed sqlite instance: %v", err)
	}

	if err := migrate(ctx, sqliteDB, pgPool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var (
		instanceID, instanceType, az, privateIP, status string
		maxContainers, runningContainers                int
		pgCreatedAt, pgUpdatedAt                        time.Time
	)
	err = pgPool.QueryRow(ctx, `SELECT
		instance_id, instance_type, availability_zone, private_ip,
		status, max_containers, running_containers,
		created_at, updated_at
	FROM instances WHERE instance_id = 'i-mig001'`).Scan(
		&instanceID, &instanceType, &az, &privateIP,
		&status, &maxContainers, &runningContainers,
		&pgCreatedAt, &pgUpdatedAt,
	)
	if err != nil {
		t.Fatalf("query postgres: %v", err)
	}

	if instanceID != "i-mig001" {
		t.Errorf("instance_id = %q", instanceID)
	}
	if instanceType != "m7g.xlarge" {
		t.Errorf("instance_type = %q", instanceType)
	}
	if az != "us-east-1a" {
		t.Errorf("availability_zone = %q", az)
	}
	if privateIP != "10.0.1.5" {
		t.Errorf("private_ip = %q", privateIP)
	}
	if status != "running" {
		t.Errorf("status = %q", status)
	}
	if maxContainers != 4 {
		t.Errorf("max_containers = %d, want 4", maxContainers)
	}
	if runningContainers != 2 {
		t.Errorf("running_containers = %d, want 2", runningContainers)
	}

	wantCreated, _ := time.Parse(time.RFC3339, createdAt)
	if !pgCreatedAt.Equal(wantCreated) {
		t.Errorf("created_at = %v, want %v", pgCreatedAt, wantCreated)
	}
	wantUpdated, _ := time.Parse(time.RFC3339, updatedAt)
	if !pgUpdatedAt.Equal(wantUpdated) {
		t.Errorf("updated_at = %v, want %v", pgUpdatedAt, wantUpdated)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	ctx := context.Background()
	sqliteDB := newTestSQLiteDB(t)
	pgPool := newTestPgPool(t)

	// Seed one row in each table.
	_, err := sqliteDB.Exec(`INSERT INTO tasks (
		id, repo_url, prompt, created_at, updated_at
	) VALUES ('bf_IDEM01', 'https://github.com/test/repo', 'test', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	_, err = sqliteDB.Exec(`INSERT INTO instances (
		instance_id, instance_type, created_at, updated_at
	) VALUES ('i-idem01', 'm7g.xlarge', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	// First run.
	if err := migrate(ctx, sqliteDB, pgPool); err != nil {
		t.Fatalf("first migrate: %v", err)
	}

	// Second run — should succeed with no duplicates.
	if err := migrate(ctx, sqliteDB, pgPool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	// Verify exactly one row per table.
	for _, table := range []string{"tasks", "instances"} {
		var count int
		err := pgPool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count)
		if err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s: count = %d, want 1", table, count)
		}
	}
}
