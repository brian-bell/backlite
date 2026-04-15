package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

// ErrStoreFailure is returned when the store fails to persist a task.
// Callers can use errors.Is(err, ErrStoreFailure) to distinguish store
// errors from validation errors returned by NewTask.
var ErrStoreFailure = errors.New("failed to create task")

// NewTask validates the request, applies config defaults, persists the task, and emits
// a task.created event. Validation errors are returned as-is with user-friendly messages.
// Store errors wrap ErrStoreFailure.
func NewTask(ctx context.Context, req *models.CreateTaskRequest, s store.Store, cfg *config.Config, bus notify.Emitter) (*models.Task, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	task := &models.Task{
		ID:            "bf_" + ulid.Make().String(),
		Status:        models.TaskStatusPending,
		TaskMode:      models.TaskModeAuto,
		Harness:       models.Harness(req.Harness),
		Prompt:        req.Prompt,
		Context:       req.Context,
		Model:         req.Model,
		Effort:        req.Effort,
		MaxBudgetUSD:  req.MaxBudgetUSD,
		MaxRuntimeSec: req.MaxRuntimeSec,
		MaxTurns:      req.MaxTurns,
		PRTitle:       req.PRTitle,
		PRBody:        req.PRBody,
		AllowedTools:  req.AllowedTools,
		ClaudeMD:      req.ClaudeMD,
		EnvVars:       req.EnvVars,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	cfg.TaskDefaults(models.TaskModeAuto).Apply(task, &config.BoolOverrides{
		CreatePR:        req.CreatePR,
		SelfReview:      req.SelfReview,
		SaveAgentOutput: req.SaveAgentOutput,
	})

	if err := s.CreateTask(ctx, task); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreFailure, err)
	}

	if bus != nil {
		bus.Emit(notify.NewEvent(notify.EventTaskCreated, task))
	}

	return task, nil
}

// NewReadTask validates the request, applies read-mode defaults (reader image
// and tighter budget/runtime/turn caps), persists the task, and emits a
// task.created event.
//
// Honored request fields: Prompt, Context, Model, Harness, Effort,
// MaxBudgetUSD, MaxRuntimeSec, MaxTurns, AllowedTools, ClaudeMD, EnvVars,
// SaveAgentOutput.
//
// Ignored request fields: PRTitle, PRBody, CreatePR, SelfReview — read tasks
// never clone repos or open PRs, so these fields have no effect.
func NewReadTask(ctx context.Context, req *models.CreateTaskRequest, s store.Store, cfg *config.Config, bus notify.Emitter) (*models.Task, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	task := &models.Task{
		ID:            "bf_" + ulid.Make().String(),
		Status:        models.TaskStatusPending,
		TaskMode:      models.TaskModeRead,
		Harness:       models.Harness(req.Harness),
		Prompt:        req.Prompt,
		Context:       req.Context,
		Model:         req.Model,
		Effort:        req.Effort,
		MaxBudgetUSD:  req.MaxBudgetUSD,
		MaxRuntimeSec: req.MaxRuntimeSec,
		MaxTurns:      req.MaxTurns,
		AllowedTools:  req.AllowedTools,
		ClaudeMD:      req.ClaudeMD,
		EnvVars:       req.EnvVars,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	cfg.TaskDefaults(models.TaskModeRead).Apply(task, &config.BoolOverrides{
		SaveAgentOutput: req.SaveAgentOutput,
	})

	if err := s.CreateTask(ctx, task); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreFailure, err)
	}

	if bus != nil {
		bus.Emit(notify.NewEvent(notify.EventTaskCreated, task))
	}

	return task, nil
}
