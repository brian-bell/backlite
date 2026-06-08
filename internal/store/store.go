package store

import (
	"context"
	"errors"

	"github.com/brian-bell/backlite/internal/models"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// TaskResult holds the fields set when a task finishes (completed or failed).
type TaskResult struct {
	Status         models.TaskStatus
	Error          string
	PRURL          string
	OutputURL      string
	CostUSD        float64
	ElapsedTimeSec int
	RepoURL        string
	TargetBranch   string
	TaskMode       string
}

// TaskFilter controls listing behavior.
type TaskFilter struct {
	Status *models.TaskStatus
	Limit  int
	Offset int
}

// Store is the persistence interface for tasks and api keys.
type Store interface {
	// Tasks
	CreateTask(ctx context.Context, task *models.Task) error
	GetTask(ctx context.Context, id string) (*models.Task, error)
	ListTasks(ctx context.Context, filter TaskFilter) ([]*models.Task, error)
	DeleteTask(ctx context.Context, id string) error

	// Named task updates
	UpdateTaskStatus(ctx context.Context, id string, status models.TaskStatus, taskErr string) error
	AssignTask(ctx context.Context, id string) error
	StartTask(ctx context.Context, id string, containerID string, agentImage string) error
	CompleteTask(ctx context.Context, id string, result TaskResult) error
	RequeueTask(ctx context.Context, id string, reason string) error
	CancelTask(ctx context.Context, id string) error
	ClearTaskAssignment(ctx context.Context, id string) error
	MarkReadyForRetry(ctx context.Context, id string) error
	RetryTask(ctx context.Context, id string, maxRetries int) error

	// API keys
	HasAPIKeys(ctx context.Context) (bool, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*models.APIKey, error)
	CreateAPIKey(ctx context.Context, key *models.APIKey) error

	// Transactions
	WithTx(ctx context.Context, fn func(Store) error) error

	Close() error
}
