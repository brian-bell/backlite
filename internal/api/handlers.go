package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

// LogFetcher retrieves container logs for a running task.
type LogFetcher interface {
	GetLogs(ctx context.Context, instanceID, containerID string, tail int) (string, error)
}

type Handlers struct {
	store  store.Store
	config *config.Config
	logs   LogFetcher
}

func NewHandlers(s store.Store, cfg *config.Config, logs LogFetcher) *Handlers {
	return &Handlers{store: s, config: cfg, logs: logs}
}

func (h *Handlers) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req models.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	taskMode := withDefault(req.TaskMode, models.TaskModeCode)
	task := &models.Task{
		ID:             "bf_" + ulid.Make().String(),
		Status:         models.TaskStatusPending,
		TaskMode:       taskMode,
		RepoURL:        req.RepoURL,
		Branch:         req.Branch,
		TargetBranch:   req.TargetBranch,
		ReviewPRNumber: req.ReviewPRNumber,
		Prompt:         req.Prompt,
		Context:        req.Context,
		Model:          withDefault(req.Model, h.config.DefaultModel),
		Effort:         withDefault(req.Effort, h.config.DefaultEffort),
		MaxBudgetUSD:   withDefaultFloat(req.MaxBudgetUSD, h.config.DefaultMaxBudget),
		MaxRuntimeMin:  withDefaultInt(req.MaxRuntimeMin, int(h.config.DefaultMaxRuntime.Minutes())),
		MaxTurns:       withDefaultInt(req.MaxTurns, h.config.DefaultMaxTurns),
		CreatePR:       req.CreatePR,
		SelfReview:     req.SelfReview,
		PRTitle:        req.PRTitle,
		PRBody:         req.PRBody,
		AllowedTools:   req.AllowedTools,
		ClaudeMD:       req.ClaudeMD,
		EnvVars:        req.EnvVars,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := h.store.CreateTask(r.Context(), task); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create task")
		return
	}

	writeJSON(w, http.StatusCreated, task)
}

func (h *Handlers) GetTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (h *Handlers) ListTasks(w http.ResponseWriter, r *http.Request) {
	filter := store.TaskFilter{
		Limit:  50,
		Offset: 0,
	}
	if s := r.URL.Query().Get("status"); s != "" {
		status := models.TaskStatus(s)
		filter.Status = &status
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			filter.Limit = n
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if n, err := strconv.Atoi(o); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	tasks, err := h.store.ListTasks(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}
	if tasks == nil {
		tasks = []*models.Task{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Handlers) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	// If running, mark as cancelled (orchestrator will kill the container)
	if task.Status == models.TaskStatusRunning || task.Status == models.TaskStatusProvisioning || task.Status == models.TaskStatusRecovering {
		task.Status = models.TaskStatusCancelled
		now := time.Now().UTC()
		task.CompletedAt = &now
		if err := h.store.UpdateTask(r.Context(), task); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to cancel task")
			return
		}
	} else if !task.Status.IsTerminal() {
		if err := h.store.DeleteTask(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete task")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) GetTaskLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	if task.InstanceID == "" || task.ContainerID == "" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		msg := "status: " + string(task.Status) + "\n"
		if task.Error != "" {
			msg += "error: " + task.Error + "\n"
		}
		w.Write([]byte(msg))
		return
	}

	tail := 100
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			tail = n
		}
	}

	logs, err := h.logs.GetLogs(r.Context(), task.InstanceID, task.ContainerID, tail)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to fetch logs: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(logs))
}

func (h *Handlers) HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"auth_mode": string(h.config.AuthMode),
	})
}

func withDefault(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}

func withDefaultFloat(val, fallback float64) float64 {
	if val == 0 {
		return fallback
	}
	return val
}

func withDefaultInt(val, fallback int) int {
	if val == 0 {
		return fallback
	}
	return val
}
