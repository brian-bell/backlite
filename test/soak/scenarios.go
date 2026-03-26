package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// scenario defines a weighted task type for the soak test.
type scenario struct {
	Name        string
	Weight      int
	FakeOutcome string
	MultiStep   bool // requires follow-up actions (cancel, retry)
}

var scenarioTable = []scenario{
	{"success", 50, "success", false},
	{"slow_success", 10, "slow_success", false},
	{"fail", 10, "fail", false},
	{"needs_input", 5, "needs_input", false},
	{"cancel", 10, "timeout", true},
	{"retry_cycle", 10, "fail", true},
	{"retry_limit", 5, "fail", true},
}

var scenarioWeightTotal int

func init() {
	for _, s := range scenarioTable {
		scenarioWeightTotal += s.Weight
	}
}

func pickScenario() scenario {
	r := rand.Intn(scenarioWeightTotal)
	for _, s := range scenarioTable {
		r -= s.Weight
		if r < 0 {
			return s
		}
	}
	return scenarioTable[0]
}

// scenarioStats tracks outcomes of multi-step scenarios.
type scenarioStats struct {
	mu   sync.Mutex
	data map[string]*ScenarioOutcome
}

func newScenarioStats() *scenarioStats {
	return &scenarioStats{data: make(map[string]*ScenarioOutcome)}
}

func (s *scenarioStats) get(name string) *ScenarioOutcome {
	if _, ok := s.data[name]; !ok {
		s.data[name] = &ScenarioOutcome{}
	}
	return s.data[name]
}

func (s *scenarioStats) recordAttempt(name string) {
	s.mu.Lock()
	s.get(name).Attempted++
	s.mu.Unlock()
}

func (s *scenarioStats) recordPass(name string) {
	s.mu.Lock()
	s.get(name).Passed++
	s.mu.Unlock()
}

func (s *scenarioStats) recordFail(name string) {
	s.mu.Lock()
	s.get(name).Failed++
	s.mu.Unlock()
}

func (s *scenarioStats) snapshot() map[string]ScenarioOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := make(map[string]ScenarioOutcome, len(s.data))
	for k, v := range s.data {
		snap[k] = *v
	}
	return snap
}

// --- API helpers ---

type taskResponse struct {
	Data struct {
		ID             string `json:"id"`
		Status         string `json:"status"`
		ReadyForRetry  bool   `json:"ready_for_retry"`
		UserRetryCount int    `json:"user_retry_count"`
	} `json:"data"`
}

func createTask(client *http.Client, apiURL, fakeOutcome string) (string, error) {
	body := fmt.Sprintf(`{"prompt":"soak test (%s)","save_agent_output":false,"env_vars":{"FAKE_OUTCOME":"%s"}}`, fakeOutcome, fakeOutcome)
	resp, err := client.Post(apiURL+"/api/v1/tasks", "application/json", bytes.NewBufferString(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		io.Copy(io.Discard, resp.Body)
		return "", fmt.Errorf("create task: status %d", resp.StatusCode)
	}

	var tr taskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("decode create response: %w", err)
	}
	return tr.Data.ID, nil
}

func getTask(client *http.Client, apiURL, taskID string) (taskResponse, error) {
	var tr taskResponse
	resp, err := client.Get(apiURL + "/api/v1/tasks/" + taskID)
	if err != nil {
		return tr, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return tr, fmt.Errorf("decode task: %w", err)
	}
	return tr, nil
}

func deleteTask(client *http.Client, apiURL, taskID string) error {
	req, err := http.NewRequest(http.MethodDelete, apiURL+"/api/v1/tasks/"+taskID, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete task: status %d", resp.StatusCode)
	}
	return nil
}

func postRetryTask(client *http.Client, apiURL, taskID string) error {
	resp, err := client.Post(apiURL+"/api/v1/tasks/"+taskID+"/retry", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("retry task: status %d", resp.StatusCode)
	}
	return nil
}

func waitForStatus(ctx context.Context, client *http.Client, apiURL, taskID string, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tr, err := getTask(client, apiURL, taskID)
		if err == nil {
			if tr.Data.Status == want {
				return nil
			}
			if isTerminal(tr.Data.Status) && tr.Data.Status != want {
				return fmt.Errorf("task %s reached %q, wanted %q", taskID, tr.Data.Status, want)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for task %s status %q", taskID, want)
}

func waitForReadyForRetry(ctx context.Context, client *http.Client, apiURL, taskID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tr, err := getTask(client, apiURL, taskID)
		if err == nil && tr.Data.ReadyForRetry {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for task %s ready_for_retry", taskID)
}

func isTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// --- Multi-step scenario runners ---
//
// Each runner manages the full lifecycle of a scenario. They run in goroutines
// and check ctx.Err() before recording failures so that scenarios interrupted
// by the test deadline don't pollute stats.

// runCancelScenario: create timeout task → wait for running → cancel → wait for cancelled.
func runCancelScenario(ctx context.Context, client *http.Client, apiURL string, stats *scenarioStats) {
	stats.recordAttempt("cancel")

	taskID, err := createTask(client, apiURL, "timeout")
	if err != nil {
		fmt.Printf("  [cancel] create failed: %v\n", err)
		stats.recordFail("cancel")
		return
	}

	if err := waitForStatus(ctx, client, apiURL, taskID, "running", 120*time.Second); err != nil {
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("  [cancel] %s wait for running: %v\n", taskID, err)
		stats.recordFail("cancel")
		return
	}

	if err := deleteTask(client, apiURL, taskID); err != nil {
		fmt.Printf("  [cancel] %s delete: %v\n", taskID, err)
		stats.recordFail("cancel")
		return
	}

	if err := waitForStatus(ctx, client, apiURL, taskID, "cancelled", 120*time.Second); err != nil {
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("  [cancel] %s wait for cancelled: %v\n", taskID, err)
		stats.recordFail("cancel")
		return
	}

	stats.recordPass("cancel")
	fmt.Printf("  [cancel] %s ok\n", taskID)
}

// runRetryCycleScenario: create failing task → wait for failed → retry → wait for failed again.
func runRetryCycleScenario(ctx context.Context, client *http.Client, apiURL string, stats *scenarioStats) {
	stats.recordAttempt("retry_cycle")

	taskID, err := createTask(client, apiURL, "fail")
	if err != nil {
		stats.recordFail("retry_cycle")
		return
	}

	if err := waitForStatus(ctx, client, apiURL, taskID, "failed", 120*time.Second); err != nil {
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("  [retry_cycle] %s wait for failed: %v\n", taskID, err)
		stats.recordFail("retry_cycle")
		return
	}

	if err := waitForReadyForRetry(ctx, client, apiURL, taskID, 120*time.Second); err != nil {
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("  [retry_cycle] %s wait for ready: %v\n", taskID, err)
		stats.recordFail("retry_cycle")
		return
	}

	if err := postRetryTask(client, apiURL, taskID); err != nil {
		fmt.Printf("  [retry_cycle] %s retry: %v\n", taskID, err)
		stats.recordFail("retry_cycle")
		return
	}

	// Task fails again with the same FAKE_OUTCOME
	if err := waitForStatus(ctx, client, apiURL, taskID, "failed", 120*time.Second); err != nil {
		if ctx.Err() != nil {
			return
		}
		fmt.Printf("  [retry_cycle] %s wait for re-fail: %v\n", taskID, err)
		stats.recordFail("retry_cycle")
		return
	}

	stats.recordPass("retry_cycle")
	fmt.Printf("  [retry_cycle] %s ok\n", taskID)
}

// runRetryLimitScenario: create failing task → retry maxRetries times →
// verify the next retry is rejected by the API.
func runRetryLimitScenario(ctx context.Context, client *http.Client, apiURL string, maxRetries int, stats *scenarioStats) {
	stats.recordAttempt("retry_limit")

	taskID, err := createTask(client, apiURL, "fail")
	if err != nil {
		stats.recordFail("retry_limit")
		return
	}

	// Exhaust all allowed retries.
	for i := 0; i < maxRetries; i++ {
		if err := waitForStatus(ctx, client, apiURL, taskID, "failed", 120*time.Second); err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Printf("  [retry_limit] %s wait for failed (round %d): %v\n", taskID, i+1, err)
			stats.recordFail("retry_limit")
			return
		}
		if err := waitForReadyForRetry(ctx, client, apiURL, taskID, 120*time.Second); err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Printf("  [retry_limit] %s wait for ready (round %d): %v\n", taskID, i+1, err)
			stats.recordFail("retry_limit")
			return
		}
		if err := postRetryTask(client, apiURL, taskID); err != nil {
			fmt.Printf("  [retry_limit] %s retry (round %d): %v\n", taskID, i+1, err)
			stats.recordFail("retry_limit")
			return
		}
	}

	// After exhausting retries, wait for the final failure.
	if err := waitForStatus(ctx, client, apiURL, taskID, "failed", 120*time.Second); err != nil {
		if ctx.Err() != nil {
			return
		}
		stats.recordFail("retry_limit")
		return
	}

	// Wait for ready_for_retry (the orchestrator still sets it).
	if err := waitForReadyForRetry(ctx, client, apiURL, taskID, 120*time.Second); err != nil {
		if ctx.Err() != nil {
			return
		}
		// May timeout if the event signals limit reached differently — not a hard failure.
	}

	// Attempt one more retry — the API should reject it.
	err = postRetryTask(client, apiURL, taskID)
	if err != nil {
		// Expected: retry rejected at limit.
		stats.recordPass("retry_limit")
		fmt.Printf("  [retry_limit] %s ok (rejected after %d retries)\n", taskID, maxRetries)
		return
	}

	// If retry succeeded, the limit wasn't enforced.
	fmt.Printf("  [retry_limit] %s retry accepted beyond limit!\n", taskID)
	stats.recordFail("retry_limit")
}
