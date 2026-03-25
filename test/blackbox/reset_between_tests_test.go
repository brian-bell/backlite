//go:build !nocontainers

package blackbox_test

import (
	"strings"
	"testing"
	"time"
)

func TestResetBetweenTests_DrainsLiveTaskState(t *testing.T) {
	t.Run("leave_slow_task_running", func(t *testing.T) {
		resetBetweenTests(t)
		t.Cleanup(dumpLogsOnFailure(t))

		task := client.CreateTask(t, map[string]any{
			"prompt": "leave task running for resetBetweenTests",
			"env_vars": map[string]string{
				"FAKE_OUTCOME": "slow_success",
			},
		})

		taskID, ok := task["id"].(string)
		if !ok || taskID == "" {
			t.Fatal("expected non-empty task ID")
		}
		if !strings.HasPrefix(taskID, "bf_") {
			t.Fatalf("task ID %q should have bf_ prefix", taskID)
		}

		client.WaitForStatus(t, taskID, "running", 15*time.Second)
	})

	t.Run("reset_waits_for_cleanup_then_reuses_capacity", func(t *testing.T) {
		start := time.Now()
		resetBetweenTests(t)
		elapsed := time.Since(start)

		// If resetBetweenTests returned immediately, the previous task was still
		// occupying the orchestrator's in-memory capacity and the next task would
		// be able to get stuck pending.
		if elapsed < 1*time.Second {
			t.Fatalf("resetBetweenTests returned too quickly: %s", elapsed)
		}

		t.Cleanup(dumpLogsOnFailure(t))

		task := client.CreateTask(t, map[string]any{
			"prompt": "fresh task after reset",
			"env_vars": map[string]string{
				"FAKE_OUTCOME": "success",
			},
		})

		taskID, ok := task["id"].(string)
		if !ok || taskID == "" {
			t.Fatal("expected non-empty task ID")
		}
		if !strings.HasPrefix(taskID, "bf_") {
			t.Fatalf("task ID %q should have bf_ prefix", taskID)
		}

		completed := client.WaitForStatus(t, taskID, "completed", 60*time.Second)
		if completed["status"] != "completed" {
			t.Fatalf("final status = %q, want completed", completed["status"])
		}
	})
}
