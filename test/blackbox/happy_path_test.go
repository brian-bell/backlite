//go:build !nocontainers

package blackbox_test

import (
	"strings"
	"testing"
	"time"
)

func TestHappyPath(t *testing.T) {
	resetBetweenTests(t)
	t.Cleanup(dumpLogsOnFailure(t))

	// Create a task with FAKE_OUTCOME=success passed via env_vars.
	// The fake agent image reads this env var and simulates a successful run,
	// writing status.json with complete=true, cost_usd=0, elapsed_time_sec=1.
	task := client.CreateTask(t, map[string]any{
		"prompt":   "test happy path",
		"env_vars": map[string]string{"FAKE_OUTCOME": "success"},
	})

	taskID, ok := task["id"].(string)
	if !ok || taskID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if !strings.HasPrefix(taskID, "bf_") {
		t.Errorf("task ID %q should have bf_ prefix", taskID)
	}
	if task["status"] != "pending" {
		t.Errorf("initial status = %q, want pending", task["status"])
	}
	if task["prompt"] != "test happy path" {
		t.Errorf("prompt = %q, want %q", task["prompt"], "test happy path")
	}

	// Wait for the task to reach completed status. The orchestrator polls every
	// 1s (BACKFLOW_POLL_INTERVAL_SEC=1), so this should complete within a few
	// poll cycles + container startup time.
	completed := client.WaitForStatus(t, taskID, "completed", 60*time.Second)

	if completed["status"] != "completed" {
		t.Errorf("final status = %q, want completed", completed["status"])
	}

	// The fake agent writes elapsed_time_sec=1 in status.json. The orchestrator
	// may fall back to wall-clock elapsed if the agent value is 0, but here it
	// should use the agent's value (1) or compute a higher wall-clock value.
	elapsedRaw, ok := completed["elapsed_time_sec"].(float64)
	if !ok {
		t.Errorf("elapsed_time_sec not present or not a number: %v", completed["elapsed_time_sec"])
	} else if elapsedRaw < 1 {
		t.Errorf("elapsed_time_sec = %v, want >= 1", elapsedRaw)
	}

	// cost_usd: the fake agent writes 0, and the Task struct uses omitempty,
	// so cost_usd may be absent (zero value is omitted). If present, verify it
	// is a valid number.
	if costRaw, ok := completed["cost_usd"]; ok {
		if _, isNum := costRaw.(float64); !isNum {
			t.Errorf("cost_usd is not a number: %v (%T)", costRaw, costRaw)
		}
	}

	// Error should be empty on success.
	if errMsg, _ := completed["error"].(string); errMsg != "" {
		t.Errorf("error = %q, want empty", errMsg)
	}

	// Webhook assertions: wait for the task.completed event to arrive.
	// The event bus delivers asynchronously, so it may lag behind the DB update.
	listener.WaitForEventType(t, taskID, "task.completed", 10*time.Second)

	events := listener.EventsForTask(taskID)
	if len(events) < 2 {
		t.Fatalf("got %d webhook events, want >= 2 (task.running + task.completed)", len(events))
	}

	// Find task.running and task.completed events and verify ordering.
	var runningIdx, completedIdx int = -1, -1
	for i, e := range events {
		if e.TaskID != taskID {
			t.Errorf("event %d has task_id %q, want %q", i, e.TaskID, taskID)
		}
		switch e.Event {
		case "task.running":
			if runningIdx == -1 {
				runningIdx = i
			}
		case "task.completed":
			if completedIdx == -1 {
				completedIdx = i
			}
		}
	}

	if runningIdx == -1 {
		t.Error("missing task.running webhook event")
	}
	if completedIdx == -1 {
		t.Error("missing task.completed webhook event")
	}
	if runningIdx != -1 && completedIdx != -1 && runningIdx >= completedIdx {
		t.Errorf("task.running (index %d) should come before task.completed (index %d)", runningIdx, completedIdx)
	}
}
