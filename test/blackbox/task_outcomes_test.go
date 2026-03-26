//go:build !nocontainers

package blackbox_test

import (
	"strings"
	"testing"
	"time"
)

func taskIDFromCreate(t *testing.T, task map[string]any) string {
	t.Helper()
	taskID, ok := task["id"].(string)
	if !ok || taskID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if !strings.HasPrefix(taskID, "bf_") {
		t.Fatalf("task ID %q should have bf_ prefix", taskID)
	}
	return taskID
}

func waitForWebhookEventCount(t *testing.T, taskID, eventType string, want int, timeout time.Duration) []WebhookEvent {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events := listener.EventsForTask(taskID)
		var filtered []WebhookEvent
		for _, event := range events {
			if event.Event == eventType {
				filtered = append(filtered, event)
			}
		}
		if len(filtered) >= want {
			return filtered
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %d %q events for task %s", want, eventType, taskID)
	return nil
}

func waitForStatusWithoutStuckTimeout(t *testing.T, taskID, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus string
	for time.Now().Before(deadline) {
		task := client.GetTask(t, taskID)
		current, _ := task["status"].(string)
		if current == want {
			return task
		}
		lastStatus = current
		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for task %s to reach status %q (last: %q, waited %s)", taskID, want, lastStatus, timeout)
	return nil
}

func TestAgentFailure(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	task := client.CreateTask(t, map[string]any{
		"prompt":   "test agent failure",
		"env_vars": map[string]string{"FAKE_OUTCOME": "fail"},
	})
	taskID := taskIDFromCreate(t, task)

	completed := waitForStatusWithoutStuckTimeout(t, taskID, "failed", 60*time.Second)
	if completed["status"] != "failed" {
		t.Fatalf("final status = %q, want failed", completed["status"])
	}
	if errMsg, _ := completed["error"].(string); errMsg != "fake agent failure" {
		t.Fatalf("error = %q, want fake agent failure", errMsg)
	}

	listener.WaitForEventType(t, taskID, "task.failed", 10*time.Second)
}

func TestNeedsInput(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	task := client.CreateTask(t, map[string]any{
		"prompt":   "test needs input",
		"env_vars": map[string]string{"FAKE_OUTCOME": "needs_input"},
	})
	taskID := taskIDFromCreate(t, task)

	completed := waitForStatusWithoutStuckTimeout(t, taskID, "failed", 60*time.Second)
	if completed["status"] != "failed" {
		t.Fatalf("final status = %q, want failed", completed["status"])
	}
	if errMsg, _ := completed["error"].(string); errMsg != "agent needs input" {
		t.Fatalf("error = %q, want agent needs input", errMsg)
	}

	event := listener.WaitForEventType(t, taskID, "task.needs_input", 10*time.Second)
	if event.Message != "fake question" {
		t.Fatalf("webhook message = %q, want fake question", event.Message)
	}
}

func TestCrash(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	task := client.CreateTask(t, map[string]any{
		"prompt":   "test crash",
		"env_vars": map[string]string{"FAKE_OUTCOME": "crash"},
	})
	taskID := taskIDFromCreate(t, task)

	completed := client.WaitForStatus(t, taskID, "failed", 60*time.Second)
	if completed["status"] != "failed" {
		t.Fatalf("final status = %q, want failed", completed["status"])
	}
	errMsg, _ := completed["error"].(string)
	if !strings.Contains(errMsg, "container exited with code 137") {
		t.Fatalf("error = %q, want exit code 137", errMsg)
	}

	listener.WaitForEventType(t, taskID, "task.failed", 10*time.Second)
}

func TestTimeout(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	task := client.CreateTask(t, map[string]any{
		"prompt":          "test timeout",
		"max_runtime_sec": 3,
		"env_vars":        map[string]string{"FAKE_OUTCOME": "timeout"},
	})
	taskID := taskIDFromCreate(t, task)

	completed := waitForStatusWithoutStuckTimeout(t, taskID, "failed", 60*time.Second)
	if completed["status"] != "failed" {
		t.Fatalf("final status = %q, want failed", completed["status"])
	}
	errMsg, _ := completed["error"].(string)
	if !strings.Contains(errMsg, "exceeded max runtime") {
		t.Fatalf("error = %q, want max runtime exceeded", errMsg)
	}

	listener.WaitForEventType(t, taskID, "task.failed", 10*time.Second)
}

func TestCancellation(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	task := client.CreateTask(t, map[string]any{
		"prompt":   "test cancellation",
		"env_vars": map[string]string{"FAKE_OUTCOME": "timeout"},
	})
	taskID := taskIDFromCreate(t, task)

	client.WaitForStatus(t, taskID, "running", 30*time.Second)
	client.DeleteTask(t, taskID)

	cancelled := client.WaitForStatus(t, taskID, "cancelled", 30*time.Second)
	if cancelled["status"] != "cancelled" {
		t.Fatalf("final status = %q, want cancelled", cancelled["status"])
	}

	listener.WaitForEventType(t, taskID, "task.cancelled", 10*time.Second)
	waitForOrchestratorIdle(t, 60*time.Second)
}

func TestCancelAndRetry(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	// Create a long-running task (timeout outcome sleeps forever)
	task := client.CreateTask(t, map[string]any{
		"prompt":   "test cancel and retry",
		"env_vars": map[string]string{"FAKE_OUTCOME": "timeout"},
	})
	taskID := taskIDFromCreate(t, task)

	// Wait for it to start running
	client.WaitForStatus(t, taskID, "running", 30*time.Second)

	// Cancel it
	client.DeleteTask(t, taskID)

	// Wait for cancelled status and cleanup (ready_for_retry becomes true).
	// Docker stop uses a 30s timeout before SIGKILL, so allow 60s for cleanup.
	client.WaitForStatus(t, taskID, "cancelled", 30*time.Second)
	client.WaitForReadyForRetry(t, taskID, 60*time.Second)

	// Retry via REST API
	retried := client.RetryTask(t, taskID)
	if retried["status"] != "pending" {
		t.Fatalf("after retry: status = %q, want pending", retried["status"])
	}

	// Wait for it to start running again (still timeout outcome)
	client.WaitForStatus(t, taskID, "running", 60*time.Second)

	// Verify retry_count incremented
	running := client.GetTask(t, taskID)
	retryCount, _ := running["retry_count"].(float64)
	if retryCount != 1 {
		t.Fatalf("retry_count = %v, want 1", retryCount)
	}
	userRetryCount, _ := running["user_retry_count"].(float64)
	if userRetryCount != 1 {
		t.Fatalf("user_retry_count = %v, want 1", userRetryCount)
	}

	// Cancel again and wait for idle to clean up
	client.DeleteTask(t, taskID)
	waitForOrchestratorIdle(t, 60*time.Second)
}

func TestWebhookResilience(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	listener.SetBehaviorForEvent("task.completed", []int{500, 500, 200}, 0)

	task := client.CreateTask(t, map[string]any{
		"prompt":   "test webhook retries",
		"env_vars": map[string]string{"FAKE_OUTCOME": "success"},
	})
	taskID := taskIDFromCreate(t, task)

	completed := client.WaitForStatus(t, taskID, "completed", 60*time.Second)
	if completed["status"] != "completed" {
		t.Fatalf("final status = %q, want completed", completed["status"])
	}

	events := waitForWebhookEventCount(t, taskID, "task.completed", 3, 30*time.Second)
	if len(events) != 3 {
		t.Fatalf("task.completed attempts = %d, want 3", len(events))
	}

	waitForOrchestratorIdle(t, 60*time.Second)
}
