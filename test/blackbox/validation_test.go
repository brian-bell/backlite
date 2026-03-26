//go:build !nocontainers

package blackbox_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCreateTask_RejectsInvalidEnvVarKey(t *testing.T) {
	resetBetweenTests(t)

	tests := []struct {
		name    string
		envVars map[string]any
		wantSub string // substring expected in error message
	}{
		{
			name:    "key with spaces",
			envVars: map[string]any{"FOO BAR": "val"},
			wantSub: "invalid env var key",
		},
		{
			name:    "key with dash",
			envVars: map[string]any{"FOO-BAR": "val"},
			wantSub: "invalid env var key",
		},
		{
			name:    "key starting with digit",
			envVars: map[string]any{"1FOO": "val"},
			wantSub: "invalid env var key",
		},
		{
			name:    "key with command substitution",
			envVars: map[string]any{"$(whoami)": "val"},
			wantSub: "invalid env var key",
		},
		{
			name:    "key with docker flag injection",
			envVars: map[string]any{"FOO --privileged -v /:/mnt -e BAR": "val"},
			wantSub: "invalid env var key",
		},
		{
			name:    "empty key",
			envVars: map[string]any{"": "val"},
			wantSub: "invalid env var key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := client.CreateTaskRaw(t, map[string]any{
				"prompt":   "test validation",
				"env_vars": tt.envVars,
			})
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			errMsg, _ := resp["error"].(string)
			if !strings.Contains(errMsg, tt.wantSub) {
				t.Errorf("error = %q, want substring %q", errMsg, tt.wantSub)
			}
		})
	}
}

func TestCreateTask_RejectsReservedEnvVarKey(t *testing.T) {
	resetBetweenTests(t)

	reserved := []string{
		"ANTHROPIC_API_KEY",
		"GITHUB_TOKEN",
		"OPENAI_API_KEY",
		"TASK_ID",
		"AUTH_MODE",
		"REPO_URL",
		"PROMPT",
		"HARNESS",
	}

	for _, key := range reserved {
		t.Run(key, func(t *testing.T) {
			status, resp := client.CreateTaskRaw(t, map[string]any{
				"prompt":   "test reserved key",
				"env_vars": map[string]any{key: "injected"},
			})
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			errMsg, _ := resp["error"].(string)
			if !strings.Contains(errMsg, "reserved") {
				t.Errorf("error = %q, want substring %q", errMsg, "reserved")
			}
		})
	}
}

func TestCreateTask_AcceptsValidEnvVars(t *testing.T) {
	resetBetweenTests(t)

	task := client.CreateTask(t, map[string]any{
		"prompt": "test valid env vars",
		"env_vars": map[string]string{
			"MY_CUSTOM_VAR": "hello",
			"_PRIVATE":      "val",
			"FOO_123":       "bar",
			"FAKE_OUTCOME":  "success",
		},
	})

	id, _ := task["id"].(string)
	if id == "" {
		t.Fatal("expected task ID")
	}
	if task["status"] != "pending" {
		t.Errorf("status = %q, want pending", task["status"])
	}

	// Wait for completion to confirm env vars were passed through correctly
	completed := client.WaitForStatus(t, id, "completed", 60*time.Second)
	if completed["status"] != "completed" {
		t.Errorf("final status = %q, want completed", completed["status"])
	}
}
