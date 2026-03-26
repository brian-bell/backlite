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
			name:    "null byte in model",
			req:     CreateTaskRequest{Prompt: "Fix bug", Model: "claude\x00evil"},
			wantErr: true,
		},
		{
			name:    "null byte in harness",
			req:     CreateTaskRequest{Prompt: "Fix bug", Harness: "claude\x00code"},
			wantErr: true,
		},
		{
			name:    "null byte in effort",
			req:     CreateTaskRequest{Prompt: "Fix bug", Effort: "high\x00"},
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
		{
			name:    "env var key with underscore prefix",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"_FOO": "val"}},
			wantErr: false,
		},
		{
			name:    "env var key with digits",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO_123": "val"}},
			wantErr: false,
		},
		{
			name:    "env var key with spaces",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO BAR": "val"}},
			wantErr: true,
		},
		{
			name:    "env var key with dash",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO-BAR": "val"}},
			wantErr: true,
		},
		{
			name:    "env var key with equals",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO=BAR": "val"}},
			wantErr: true,
		},
		{
			name:    "env var key starting with digit",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"1FOO": "val"}},
			wantErr: true,
		},
		{
			name:    "env var key with docker flag injection",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"FOO --privileged -v /:/mnt -e BAR": "val"}},
			wantErr: true,
		},
		{
			name:    "env var key with command substitution",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"$(whoami)": "val"}},
			wantErr: true,
		},
		{
			name:    "env var key empty",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"": "val"}},
			wantErr: true,
		},
		{
			name:    "reserved env var key ANTHROPIC_API_KEY",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"ANTHROPIC_API_KEY": "sk-attacker"}},
			wantErr: true,
		},
		{
			name:    "reserved env var key GITHUB_TOKEN",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"GITHUB_TOKEN": "ghp-attacker"}},
			wantErr: true,
		},
		{
			name:    "reserved env var key OPENAI_API_KEY",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"OPENAI_API_KEY": "sk-attacker"}},
			wantErr: true,
		},
		{
			name:    "reserved env var key TASK_ID",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"TASK_ID": "bf_fake"}},
			wantErr: true,
		},
		{
			name:    "reserved env var key AUTH_MODE",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"AUTH_MODE": "none"}},
			wantErr: true,
		},
		{
			name:    "reserved env var key REPO_URL",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"REPO_URL": "https://evil.com/repo"}},
			wantErr: true,
		},
		{
			name:    "non-reserved env var key allowed",
			req:     CreateTaskRequest{Prompt: "Fix bug", EnvVars: map[string]string{"MY_CUSTOM_VAR": "val"}},
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
