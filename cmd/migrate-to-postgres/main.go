package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func main() {
	sqlitePath := os.Getenv("BACKFLOW_DB_PATH")
	pgURL := os.Getenv("BACKFLOW_DATABASE_URL")

	if sqlitePath == "" || pgURL == "" {
		fmt.Fprintln(os.Stderr, "BACKFLOW_DB_PATH and BACKFLOW_DATABASE_URL are required")
		os.Exit(1)
	}

	ctx := context.Background()

	sqliteDB, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open sqlite: %v\n", err)
		os.Exit(1)
	}
	defer sqliteDB.Close()

	pgPool, err := pgxpool.New(ctx, pgURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect postgres: %v\n", err)
		os.Exit(1)
	}
	defer pgPool.Close()

	if err := migrate(ctx, sqliteDB, pgPool); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Migration complete.")
}

func migrate(ctx context.Context, sqliteDB *sql.DB, pgPool *pgxpool.Pool) error {
	if err := migrateTasks(ctx, sqliteDB, pgPool); err != nil {
		return fmt.Errorf("tasks: %w", err)
	}
	if err := migrateInstances(ctx, sqliteDB, pgPool); err != nil {
		return fmt.Errorf("instances: %w", err)
	}
	if err := migrateSenders(ctx, sqliteDB, pgPool); err != nil {
		return fmt.Errorf("allowed_senders: %w", err)
	}
	return nil
}

func parseTimestamp(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05-07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("parsing time %q: unsupported format", s)
}

func parseNullableTimestamp(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}
	t, err := parseTimestamp(*s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func migrateTasks(ctx context.Context, sqliteDB *sql.DB, pgPool *pgxpool.Pool) error {
	rows, err := sqliteDB.QueryContext(ctx, `SELECT
		id, status, task_mode, harness, repo_url, branch, target_branch,
		prompt, context, model, effort,
		max_budget_usd, max_runtime_sec, max_turns,
		create_pr, self_review, save_agent_output,
		pr_title, pr_body, pr_url, output_url,
		allowed_tools, claude_md, env_vars,
		instance_id, container_id, retry_count,
		cost_usd, elapsed_time_sec, error, reply_channel,
		created_at, updated_at, started_at, completed_at
	FROM tasks`)
	if err != nil {
		return fmt.Errorf("query sqlite: %w", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var (
			id, status, taskMode, harness, repoURL, branch, targetBranch string
			prompt, taskContext, model, effort                           string
			maxBudgetUSD, costUSD                                        float64
			maxRuntimeMin, maxTurns                                      int
			createPR, selfReview, saveAgentOutput                        int
			prTitle, prBody, prURL, outputURL                            string
			allowedTools, claudeMD, envVars                              string
			instanceID, containerID                                      string
			retryCount, elapsedTimeSec                                   int
			errorStr, replyChannel                                       string
			createdAtStr, updatedAtStr                                   string
			startedAtStr, completedAtStr                                 *string
		)

		if err := rows.Scan(
			&id, &status, &taskMode, &harness, &repoURL, &branch, &targetBranch,
			&prompt, &taskContext, &model, &effort,
			&maxBudgetUSD, &maxRuntimeMin, &maxTurns,
			&createPR, &selfReview, &saveAgentOutput,
			&prTitle, &prBody, &prURL, &outputURL,
			&allowedTools, &claudeMD, &envVars,
			&instanceID, &containerID, &retryCount,
			&costUSD, &elapsedTimeSec, &errorStr, &replyChannel,
			&createdAtStr, &updatedAtStr, &startedAtStr, &completedAtStr,
		); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		createdAt, err := parseTimestamp(createdAtStr)
		if err != nil {
			return fmt.Errorf("parse created_at for %s: %w", id, err)
		}
		updatedAt, err := parseTimestamp(updatedAtStr)
		if err != nil {
			return fmt.Errorf("parse updated_at for %s: %w", id, err)
		}
		startedAt, err := parseNullableTimestamp(startedAtStr)
		if err != nil {
			return fmt.Errorf("parse started_at for %s: %w", id, err)
		}
		completedAt, err := parseNullableTimestamp(completedAtStr)
		if err != nil {
			return fmt.Errorf("parse completed_at for %s: %w", id, err)
		}

		_, err = pgPool.Exec(ctx, `INSERT INTO tasks (
			id, status, task_mode, harness, repo_url, branch, target_branch,
			prompt, context, model, effort,
			max_budget_usd, max_runtime_sec, max_turns,
			create_pr, self_review, save_agent_output,
			pr_title, pr_body, pr_url, output_url,
			allowed_tools, claude_md, env_vars,
			instance_id, container_id, retry_count,
			cost_usd, elapsed_time_sec, error, reply_channel,
			created_at, updated_at, started_at, completed_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14,
			$15, $16, $17,
			$18, $19, $20, $21,
			$22, $23, $24,
			$25, $26, $27,
			$28, $29, $30, $31,
			$32, $33, $34, $35
		) ON CONFLICT DO NOTHING`,
			id, status, taskMode, harness, repoURL, branch, targetBranch,
			prompt, taskContext, model, effort,
			maxBudgetUSD, maxRuntimeMin, maxTurns,
			createPR == 1, selfReview == 1, saveAgentOutput == 1,
			prTitle, prBody, prURL, outputURL,
			allowedTools, claudeMD, envVars,
			instanceID, containerID, retryCount,
			costUSD, elapsedTimeSec, errorStr, replyChannel,
			createdAt, updatedAt, startedAt, completedAt,
		)
		if err != nil {
			return fmt.Errorf("insert task %s: %w", id, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}

	log.Printf("tasks: migrated %d rows", count)
	return nil
}

func migrateInstances(ctx context.Context, sqliteDB *sql.DB, pgPool *pgxpool.Pool) error {
	rows, err := sqliteDB.QueryContext(ctx, `SELECT
		instance_id, instance_type, availability_zone, private_ip,
		status, max_containers, running_containers,
		created_at, updated_at
	FROM instances`)
	if err != nil {
		return fmt.Errorf("query sqlite: %w", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var (
			instanceID, instanceType, az, privateIP, status string
			maxContainers, runningContainers                int
			createdAtStr, updatedAtStr                      string
		)

		if err := rows.Scan(
			&instanceID, &instanceType, &az, &privateIP,
			&status, &maxContainers, &runningContainers,
			&createdAtStr, &updatedAtStr,
		); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		createdAt, err := parseTimestamp(createdAtStr)
		if err != nil {
			return fmt.Errorf("parse created_at for %s: %w", instanceID, err)
		}
		updatedAt, err := parseTimestamp(updatedAtStr)
		if err != nil {
			return fmt.Errorf("parse updated_at for %s: %w", instanceID, err)
		}

		_, err = pgPool.Exec(ctx, `INSERT INTO instances (
			instance_id, instance_type, availability_zone, private_ip,
			status, max_containers, running_containers,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT DO NOTHING`,
			instanceID, instanceType, az, privateIP,
			status, maxContainers, runningContainers,
			createdAt, updatedAt,
		)
		if err != nil {
			return fmt.Errorf("insert instance %s: %w", instanceID, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}

	log.Printf("instances: migrated %d rows", count)
	return nil
}

func migrateSenders(ctx context.Context, sqliteDB *sql.DB, pgPool *pgxpool.Pool) error {
	rows, err := sqliteDB.QueryContext(ctx, `SELECT
		channel_type, address, enabled, created_at
	FROM allowed_senders`)
	if err != nil {
		return fmt.Errorf("query sqlite: %w", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var (
			channelType, address string
			enabled              int
			createdAtStr         string
		)

		if err := rows.Scan(&channelType, &address, &enabled, &createdAtStr); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}

		createdAt, err := parseTimestamp(createdAtStr)
		if err != nil {
			return fmt.Errorf("parse created_at for %s/%s: %w", channelType, address, err)
		}

		_, err = pgPool.Exec(ctx, `INSERT INTO allowed_senders (
			channel_type, address, enabled, created_at
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`,
			channelType, address, enabled == 1, createdAt,
		)
		if err != nil {
			return fmt.Errorf("insert allowed_sender %s/%s: %w", channelType, address, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}

	log.Printf("allowed_senders: migrated %d rows", count)
	return nil
}

func runGooseMigrations(pgConnStr, migrationsDir string) error {
	db, err := goose.OpenDBWithDriver("pgx", pgConnStr)
	if err != nil {
		return fmt.Errorf("goose open: %w", err)
	}
	defer db.Close()
	if err := goose.Up(db, migrationsDir); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
