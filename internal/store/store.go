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

// ReadingMatch is a similarity-search result for an existing reading.
type ReadingMatch struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	TLDR       string  `json:"tldr"`
	URL        string  `json:"url"`
	Similarity float64 `json:"similarity"`
}

// Store is the persistence interface for tasks, api keys, and readings.
type Store interface {
	// Tasks
	CreateTask(ctx context.Context, task *models.Task) error
	GetTask(ctx context.Context, id string) (*models.Task, error)
	ListTasks(ctx context.Context, filter TaskFilter) ([]*models.Task, error)
	DeleteTask(ctx context.Context, id string) error
	CountActiveTasks(ctx context.Context) (int, error)

	// Named task updates
	UpdateTaskStatus(ctx context.Context, id string, status models.TaskStatus, taskErr string) error
	AssignTask(ctx context.Context, id string) error
	StartTask(ctx context.Context, id string, containerID string) error
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

	// Readings
	UpsertReading(ctx context.Context, r *models.Reading) error
	GetReadingByURL(ctx context.Context, url string) (*models.Reading, error)
	FindSimilarReadings(ctx context.Context, queryEmbedding []float32, limit int) ([]ReadingMatch, error)

	// Transactions
	WithTx(ctx context.Context, fn func(Store) error) error

	Close() error
}
