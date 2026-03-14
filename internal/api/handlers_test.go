package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/store"
)

func testServer(t *testing.T) http.Handler {
	t.Helper()
	f, err := os.CreateTemp("", "backflow-api-test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	s, err := store.NewSQLite(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		AuthMode:          config.AuthModeAPIKey,
		AnthropicAPIKey:   "sk-test",
		DefaultModel:      "claude-sonnet-4-6",
		DefaultMaxBudget:  10.0,
		DefaultMaxRuntime: 30 * 60e9, // 30 min
		DefaultMaxTurns:   200,
	}

	return NewServer(s, cfg)
}

func TestHealthCheck(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCreateAndGetTask(t *testing.T) {
	srv := testServer(t)

	body := `{"repo_url":"https://github.com/test/repo","prompt":"Fix the bug","create_pr":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d, body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.ID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if resp.Data.Status != "pending" {
		t.Errorf("status = %q, want pending", resp.Data.Status)
	}

	// Get the task
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+resp.Data.ID, nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("get status = %d, want %d", w2.Code, http.StatusOK)
	}
}

func TestListTasks(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Data []any `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data == nil {
		t.Error("expected non-nil data array")
	}
}

func TestCreateTaskValidation(t *testing.T) {
	srv := testServer(t)

	body := `{"repo_url":"","prompt":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestDeleteTask(t *testing.T) {
	srv := testServer(t)

	// Create a task first
	body := `{"repo_url":"https://github.com/test/repo","prompt":"Fix it"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	// Delete it
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+resp.Data.ID, nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)

	if w2.Code != http.StatusNoContent {
		t.Errorf("delete status = %d, want %d", w2.Code, http.StatusNoContent)
	}
}
