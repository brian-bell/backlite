package models

import "testing"

func TestCreateTaskRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateTaskRequest
		wantErr bool
	}{
		{
			name:    "valid prompt only",
			req:     CreateTaskRequest{Prompt: "Fix the bug in https://github.com/org/repo"},
			wantErr: false,
		},
		{
			name:    "valid with claude_code harness",
			req:     CreateTaskRequest{Prompt: "Fix bug", Harness: "claude_code"},
			wantErr: false,
		},
		{
			name:    "valid with codex harness",
			req:     CreateTaskRequest{Prompt: "Fix bug", Harness: "codex"},
			wantErr: false,
		},
		{
			name:    "invalid harness",
			req:     CreateTaskRequest{Prompt: "Fix bug", Harness: "invalid"},
			wantErr: true,
		},
		{
			name:    "missing prompt",
			req:     CreateTaskRequest{},
			wantErr: true,
		},
		{
			name:    "negative budget",
			req:     CreateTaskRequest{Prompt: "Fix", MaxBudgetUSD: -1},
			wantErr: true,
		},
		{
			name:    "negative runtime",
			req:     CreateTaskRequest{Prompt: "Fix", MaxRuntimeSec: -1},
			wantErr: true,
		},
		{
			name:    "valid effort low",
			req:     CreateTaskRequest{Prompt: "Fix", Effort: "low"},
			wantErr: false,
		},
		{
			name:    "valid effort xhigh",
			req:     CreateTaskRequest{Prompt: "Fix", Effort: "xhigh"},
			wantErr: false,
		},
		{
			name:    "invalid effort",
			req:     CreateTaskRequest{Prompt: "Fix", Effort: "ultra"},
			wantErr: true,
		},
		{
			name:    "null byte in prompt",
			req:     CreateTaskRequest{Prompt: "Fix \x00 bug"},
			wantErr: true,
		},
		{
			name:    "null byte in context",
			req:     CreateTaskRequest{Prompt: "Fix bug", Context: "has \x00 null"},
			wantErr: true,
		},
		{
			name:    "null byte in claude_md",
			req:     CreateTaskRequest{Prompt: "Fix bug", ClaudeMD: "\x00"},
			wantErr: true,
		},
		{
			name:    "null byte in allowed_tools element",
			req:     CreateTaskRequest{Prompt: "Fix bug", AllowedTools: []string{"Bash", "Read\x00Write"}},
			wantErr: true,
		},
		{
			name:    "null byte in env_vars key",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO\x00BAR": "val"}},
			wantErr: true,
		},
		{
			name:    "null byte in env_vars value",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO": "val\x00ue"}},
			wantErr: true,
		},
		{
			name:    "valid allowed_tools",
			req:     CreateTaskRequest{Prompt: "Fix bug", AllowedTools: []string{"Bash", "Read"}},
			wantErr: false,
		},
		{
			name:    "valid env_vars",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO": "bar"}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTaskStatusIsTerminal(t *testing.T) {
	terminal := []TaskStatus{TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%q should be terminal", s)
		}
	}

	nonTerminal := []TaskStatus{TaskStatusPending, TaskStatusProvisioning, TaskStatusRunning, TaskStatusInterrupted, TaskStatusRecovering}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%q should not be terminal", s)
		}
	}
}
