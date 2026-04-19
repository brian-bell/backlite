package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
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
	bus    notify.Emitter
}

func NewHandlers(s store.Store, cfg *config.Config, logs LogFetcher, bus notify.Emitter) *Handlers {
	return &Handlers{store: s, config: cfg, logs: logs, bus: bus}
}

func (h *Handlers) CreateTask(w http.ResponseWriter, r *http.Request) {
	var req models.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	task, err := NewTask(r.Context(), &req, h.store, h.config, h.bus)
	if err != nil {
		if errors.Is(err, ErrStoreFailure) {
			writeError(w, http.StatusInternalServerError, "failed to create task")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	task.RedactReplyChannel()
	writeJSON(w, http.StatusCreated, task)
}

func (h *Handlers) GetTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	task.RedactReplyChannel()
	writeJSON(w, http.StatusOK, task)
}

func (h *Handlers) ListTasks(w http.ResponseWriter, r *http.Request) {
	filter := store.TaskFilter{
		Limit: 50,
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
	for _, t := range tasks {
		t.RedactReplyChannel()
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (h *Handlers) DeleteTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	task, err := h.store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}

	// If running, mark as cancelled (orchestrator will kill the container)
	if task.Status == models.TaskStatusRunning || task.Status == models.TaskStatusProvisioning || task.Status == models.TaskStatusRecovering {
		if err := h.store.CancelTask(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to cancel task")
			return
		}
		h.bus.Emit(notify.NewEvent(notify.EventTaskCancelled, task))
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
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task")
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

func (h *Handlers) RetryTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := RetryTask(r.Context(), id, h.store, h.config.MaxUserRetries); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.bus.Emit(notify.NewEvent(notify.EventTaskRetry, &models.Task{ID: id}))
	task, err := h.store.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task after retry")
		return
	}
	task.RedactReplyChannel()
	writeJSON(w, http.StatusOK, task)
}

func (h *Handlers) GetTaskOutput(w http.ResponseWriter, r *http.Request) {
	h.serveOutputFile(w, r, "container_output.log", "text/plain; charset=utf-8")
}

func (h *Handlers) GetTaskOutputJSON(w http.ResponseWriter, r *http.Request) {
	h.serveOutputFile(w, r, "task.json", "application/json")
}

// serveOutputFile streams a single file from {DataDir}/tasks/{id}/{name}.
// Returns 404 when the file is missing (the task directory may not have been
// populated yet, or the task may not exist).
func (h *Handlers) serveOutputFile(w http.ResponseWriter, r *http.Request, name, contentType string) {
	id := chi.URLParam(r, "id")
	path := filepath.Join(h.config.DataDir, "tasks", id, name)

	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "output not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to access output file")
		return
	}

	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, path)
}

func (h *Handlers) HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}
