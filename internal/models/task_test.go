package models

import "testing"

func TestCreateTaskRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateTaskRequest
		wantErr bool
	}{
		{
			name:    "valid code mode",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo", Prompt: "Fix bug"},
			wantErr: false,
		},
		{
			name:    "valid code mode explicit",
			req:     CreateTaskRequest{TaskMode: "code", RepoURL: "https://github.com/test/repo", Prompt: "Fix bug"},
			wantErr: false,
		},
		{
			name:    "valid with claude_code harness",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo", Prompt: "Fix bug", Harness: "claude_code"},
			wantErr: false,
		},
		{
			name:    "valid with codex harness",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo", Prompt: "Fix bug", Harness: "codex"},
			wantErr: false,
		},
		{
			name:    "invalid harness",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo", Prompt: "Fix bug", Harness: "invalid"},
			wantErr: true,
		},
		{
			name:    "missing repo_url",
			req:     CreateTaskRequest{Prompt: "Fix bug"},
			wantErr: true,
		},
		{
			name:    "missing prompt in code mode",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo"},
			wantErr: true,
		},
		{
			name:    "negative budget",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo", Prompt: "Fix", MaxBudgetUSD: -1},
			wantErr: true,
		},
		{
			name:    "valid review mode",
			req:     CreateTaskRequest{TaskMode: "review", RepoURL: "https://github.com/test/repo", ReviewPRNumber: 42},
			wantErr: false,
		},
		{
			name:    "review mode with optional prompt",
			req:     CreateTaskRequest{TaskMode: "review", RepoURL: "https://github.com/test/repo", ReviewPRNumber: 10, Prompt: "Focus on security"},
			wantErr: false,
		},
		{
			name:    "review mode missing pr number",
			req:     CreateTaskRequest{TaskMode: "review", RepoURL: "https://github.com/test/repo"},
			wantErr: true,
		},
		{
			name:    "review mode negative pr number",
			req:     CreateTaskRequest{TaskMode: "review", RepoURL: "https://github.com/test/repo", ReviewPRNumber: -1},
			wantErr: true,
		},
		{
			name:    "invalid task mode",
			req:     CreateTaskRequest{TaskMode: "deploy", RepoURL: "https://github.com/test/repo", Prompt: "Fix"},
			wantErr: true,
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
