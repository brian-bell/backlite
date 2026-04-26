package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/ingest"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

// urlBearingPrompt matches a prompt that contains an http(s) URL anywhere.
// Used to reject inline_content paired with a URL-bearing prompt — the two
// source paths are exclusive, and slice 1 takes the conservative stance of
// flagging any URL occurrence rather than only a leading-token URL.
var urlBearingPrompt = regexp.MustCompile(`(?i)https?://`)

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

	if req.TaskMode != nil && *req.TaskMode == models.TaskModeRead {
		return NewReadTask(ctx, req, s, cfg, bus)
	}

	// inline_content is exclusive to read-mode tasks.
	if req.InlineContent != nil {
		return nil, fmt.Errorf("inline_content is only valid for read-mode tasks (set task_mode=\"read\")")
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
		Force:         req.Force != nil && *req.Force,
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

	if cfg.ReaderImage == "" {
		return nil, fmt.Errorf("reading mode is not configured on this server (BACKFLOW_READER_IMAGE is not set)")
	}

	if req.InlineContent != nil && urlBearingPrompt.MatchString(req.Prompt) {
		return nil, fmt.Errorf("inline_content cannot be combined with a URL-bearing prompt; pick one source or the other")
	}

	now := time.Now().UTC()

	prompt := req.Prompt
	var inlineSHA string
	if req.InlineContent != nil {
		sha, _, err := ingest.Write(cfg.DataDir, []byte(*req.InlineContent))
		if err != nil {
			return nil, fmt.Errorf("persist inline_content: %w", err)
		}
		inlineSHA = sha
		prompt = "markdown://" + sha
	}

	task := &models.Task{
		ID:                  "bf_" + ulid.Make().String(),
		Status:              models.TaskStatusPending,
		TaskMode:            models.TaskModeRead,
		Harness:             models.Harness(req.Harness),
		Prompt:              prompt,
		InlineContentSHA256: inlineSHA,
		Context:             req.Context,
		Model:               req.Model,
		Effort:              req.Effort,
		Force:               req.Force != nil && *req.Force,
		MaxBudgetUSD:        req.MaxBudgetUSD,
		MaxRuntimeSec:       req.MaxRuntimeSec,
		MaxTurns:            req.MaxTurns,
		AllowedTools:        req.AllowedTools,
		ClaudeMD:            req.ClaudeMD,
		EnvVars:             req.EnvVars,
		CreatedAt:           now,
		UpdatedAt:           now,
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
