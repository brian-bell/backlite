package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

// taskIDPattern matches Backlite task IDs: `bf_` prefix + 26-char ULID body.
// Mirrors the OpenAPI schema pattern. Used to reject malformed IDs before
// they reach code paths (like filesystem joins) where an unsafe value could
// be harmful.
var taskIDPattern = regexp.MustCompile(`^bf_[0-9A-Z]{26}$`)

// LogFetcher retrieves container logs for a running task.
type LogFetcher interface {
	GetLogs(ctx context.Context, containerID string, tail int) (string, error)
}

type Handlers struct {
	store  store.Store
	config *config.Config
	logs   LogFetcher
	bus    notify.Emitter
}

type findSimilarReadingsRequest struct {
	QueryEmbedding []float32 `json:"query_embedding"`
	MatchCount     int       `json:"match_count"`
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

	if task.ContainerID == "" {
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

	logs, err := h.logs.GetLogs(r.Context(), task.ContainerID, tail)
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
	writeJSON(w, http.StatusOK, task)
}

func (h *Handlers) GetTaskOutput(w http.ResponseWriter, r *http.Request) {
	h.serveOutputFile(w, r, "container_output.log", "text/plain; charset=utf-8")
}

func (h *Handlers) GetTaskOutputJSON(w http.ResponseWriter, r *http.Request) {
	h.serveOutputFile(w, r, "task.json", "application/json")
}

func (h *Handlers) LookupReading(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	reading, err := h.store.GetReadingByURL(r.Context(), url)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up reading")
		return
	}

	writeJSON(w, http.StatusOK, []map[string]any{{
		"id":    reading.ID,
		"url":   reading.URL,
		"title": reading.Title,
		"tldr":  reading.TLDR,
	}})
}

func (h *Handlers) FindSimilarReadings(w http.ResponseWriter, r *http.Request) {
	var req findSimilarReadingsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.QueryEmbedding) == 0 {
		writeError(w, http.StatusBadRequest, "query_embedding is required")
		return
	}
	if req.MatchCount <= 0 {
		req.MatchCount = 5
	}

	matches, err := h.store.FindSimilarReadings(r.Context(), req.QueryEmbedding, req.MatchCount)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to find similar readings")
		return
	}
	if matches == nil {
		matches = []store.ReadingMatch{}
	}
	writeJSON(w, http.StatusOK, matches)
}

// serveOutputFile streams a single file from {DataDir}/tasks/{id}/{name}.
// Returns 400 when the task ID is malformed and 404 when the task does not
// exist, the current attempt is not terminal yet, or the file is missing.
func (h *Handlers) serveOutputFile(w http.ResponseWriter, r *http.Request, name, contentType string) {
	id := chi.URLParam(r, "id")
	if !taskIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}

	task, err := h.store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "output not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if !task.Status.IsTerminal() || task.OutputURL == "" {
		writeError(w, http.StatusNotFound, "output not found")
		return
	}

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
