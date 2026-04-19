package store

import (
	"context"
	"errors"

	"github.com/backflow-labs/backflow/internal/models"
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

// Store is the persistence interface for tasks and instances.
type Store interface {
	// Tasks
	CreateTask(ctx context.Context, task *models.Task) error
	GetTask(ctx context.Context, id string) (*models.Task, error)
	ListTasks(ctx context.Context, filter TaskFilter) ([]*models.Task, error)
	DeleteTask(ctx context.Context, id string) error

	// Named task updates
	UpdateTaskStatus(ctx context.Context, id string, status models.TaskStatus, taskErr string) error
	AssignTask(ctx context.Context, id string, instanceID string) error
	StartTask(ctx context.Context, id string, containerID string) error
	CompleteTask(ctx context.Context, id string, result TaskResult) error
	RequeueTask(ctx context.Context, id string, reason string) error
	CancelTask(ctx context.Context, id string) error
	ClearTaskAssignment(ctx context.Context, id string) error
	MarkReadyForRetry(ctx context.Context, id string) error
	RetryTask(ctx context.Context, id string, maxRetries int) error

	// Instances
	CreateInstance(ctx context.Context, inst *models.Instance) error
	GetInstance(ctx context.Context, id string) (*models.Instance, error)
	ListInstances(ctx context.Context, status *models.InstanceStatus) ([]*models.Instance, error)

	// Named instance updates
	UpdateInstanceStatus(ctx context.Context, id string, status models.InstanceStatus) error
	IncrementRunningContainers(ctx context.Context, id string) error
	DecrementRunningContainers(ctx context.Context, id string) error
	UpdateInstanceDetails(ctx context.Context, id string, privateIP, az string) error
	ResetRunningContainers(ctx context.Context, id string) error

	// Discord installs
	UpsertDiscordInstall(ctx context.Context, install *models.DiscordInstall) error
	GetDiscordInstall(ctx context.Context, guildID string) (*models.DiscordInstall, error)
	DeleteDiscordInstall(ctx context.Context, guildID string) error

	// Discord task threads
	UpsertDiscordTaskThread(ctx context.Context, thread *models.DiscordTaskThread) error
	GetDiscordTaskThread(ctx context.Context, taskID string) (*models.DiscordTaskThread, error)

	// API keys
	HasAPIKeys(ctx context.Context) (bool, error)
	GetAPIKeyByHash(ctx context.Context, keyHash string) (*models.APIKey, error)
	CreateAPIKey(ctx context.Context, key *models.APIKey) error

	// Readings
	UpsertReading(ctx context.Context, r *models.Reading) error
	GetReadingByURL(ctx context.Context, url string) (*models.Reading, error)

	// Transactions
	WithTx(ctx context.Context, fn func(Store) error) error

	Close() error
}
