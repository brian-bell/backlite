//go:build !nocontainers

package blackbox_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// BackflowClient wraps net/http calls to the Backflow REST API. All methods
// accept a *testing.T and fatal on unexpected errors, keeping test code concise.
type BackflowClient struct {
	baseURL string
	http    *http.Client
}

func newBackflowClient(baseURL string) *BackflowClient {
	return &BackflowClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// CreateTask POSTs a new task and returns the unwrapped data object.
func (c *BackflowClient) CreateTask(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}

	resp, err := c.http.Post(c.baseURL+"/api/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/v1/tasks: status %d, body: %s", resp.StatusCode, respBody)
	}

	return c.unwrapData(t, resp)
}

// CreateTaskRaw POSTs a new task and returns the raw status code and response
// body. Unlike CreateTask, it does not fatal on non-201 responses, making it
// suitable for testing validation errors.
func (c *BackflowClient) CreateTaskRaw(t *testing.T, req map[string]any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}

	resp, err := c.http.Post(c.baseURL+"/api/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/v1/tasks: %v", err)
	}
	defer resp.Body.Close()

	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	result := map[string]any{"error": envelope.Error}
	if len(envelope.Data) > 0 {
		var data map[string]any
		if err := json.Unmarshal(envelope.Data, &data); err == nil {
			result["data"] = data
		}
	}
	return resp.StatusCode, result
}

// GetTask retrieves a task by ID.
func (c *BackflowClient) GetTask(t *testing.T, id string) map[string]any {
	t.Helper()
	resp, err := c.http.Get(c.baseURL + "/api/v1/tasks/" + id)
	if err != nil {
		t.Fatalf("GET /api/v1/tasks/%s: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/v1/tasks/%s: status %d, body: %s", id, resp.StatusCode, respBody)
	}

	return c.unwrapData(t, resp)
}

// ListTasks lists tasks with optional query parameters.
func (c *BackflowClient) ListTasks(t *testing.T, params url.Values) []map[string]any {
	t.Helper()
	u := c.baseURL + "/api/v1/tasks"
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	resp, err := c.http.Get(u)
	if err != nil {
		t.Fatalf("GET /api/v1/tasks: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET /api/v1/tasks: status %d, body: %s", resp.StatusCode, respBody)
	}

	return c.unwrapDataList(t, resp)
}

// DeleteTask cancels/deletes a task by ID.
func (c *BackflowClient) DeleteTask(t *testing.T, id string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, c.baseURL+"/api/v1/tasks/"+id, nil)
	if err != nil {
		t.Fatalf("create DELETE request: %v", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/v1/tasks/%s: %v", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("DELETE /api/v1/tasks/%s: status %d, body: %s", id, resp.StatusCode, respBody)
	}
}

// GetLogs retrieves the logs for a task as plain text.
func (c *BackflowClient) GetLogs(t *testing.T, id string) string {
	t.Helper()
	resp, err := c.http.Get(c.baseURL + "/api/v1/tasks/" + id + "/logs")
	if err != nil {
		t.Fatalf("GET /api/v1/tasks/%s/logs: %v", id, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read logs response: %v", err)
	}
	return string(body)
}

// stuckStateTimeout is how long a task can remain in the same non-terminal
// status before WaitForStatus fails. This catches states like "interrupted"
// that should transition (via recovery) but won't in local mode.
const stuckStateTimeout = 15 * time.Second

// WaitForStatus polls GetTask until the task reaches the desired status or the
// timeout expires. Returns the final task state.
func (c *BackflowClient) WaitForStatus(t *testing.T, id, status string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus string
	lastChange := time.Now()
	for time.Now().Before(deadline) {
		task := c.GetTask(t, id)
		current, _ := task["status"].(string)
		if current == status {
			return task
		}
		// If the task reached a terminal state that isn't the desired one, fail early.
		if isTerminal(current) && current != status {
			t.Fatalf("task %s reached terminal status %q while waiting for %q", id, current, status)
		}
		// Track how long the task has been stuck in the same status.
		if current != lastStatus {
			lastStatus = current
			lastChange = time.Now()
		} else if time.Since(lastChange) > stuckStateTimeout {
			t.Fatalf("task %s stuck in status %q for %s while waiting for %q",
				id, current, stuckStateTimeout, status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for task %s to reach status %q (last: %q, waited %s)", id, status, lastStatus, timeout)
	return nil // unreachable
}

// HealthCheck asserts the health endpoint returns 200.
func (c *BackflowClient) HealthCheck(t *testing.T) {
	t.Helper()
	resp, err := c.http.Get(c.baseURL + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET /api/v1/health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health check: status %d, want 200", resp.StatusCode)
	}
}

// unwrapData reads a JSON response with {"data": ...} envelope and returns the
// data as map[string]any.
func (c *BackflowClient) unwrapData(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error != "" {
		t.Fatalf("API error: %s", envelope.Error)
	}

	var data map[string]any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("unmarshal data: %v (raw: %s)", err, envelope.Data)
	}
	return data
}

// unwrapDataList reads a JSON response with {"data": [...]} envelope.
func (c *BackflowClient) unwrapDataList(t *testing.T, resp *http.Response) []map[string]any {
	t.Helper()
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Error != "" {
		t.Fatalf("API error: %s", envelope.Error)
	}

	var data []map[string]any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("unmarshal data list: %v (raw: %s)", err, envelope.Data)
	}
	return data
}

// isTerminal returns true for task statuses that will not change further.
// Note: "interrupted" is NOT terminal — recovery re-queues it as pending.
func isTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}
