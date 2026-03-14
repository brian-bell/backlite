package store

import (
	"context"

	"github.com/backflow-labs/backflow/internal/models"
)

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
	UpdateTask(ctx context.Context, task *models.Task) error
	DeleteTask(ctx context.Context, id string) error

	// Instances
	CreateInstance(ctx context.Context, inst *models.Instance) error
	GetInstance(ctx context.Context, id string) (*models.Instance, error)
	ListInstances(ctx context.Context, status *models.InstanceStatus) ([]*models.Instance, error)
	UpdateInstance(ctx context.Context, inst *models.Instance) error

	Close() error
}
