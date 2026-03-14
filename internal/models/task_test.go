package models

import "testing"

func TestCreateTaskRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateTaskRequest
		wantErr bool
	}{
		{
			name:    "valid",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo", Prompt: "Fix bug"},
			wantErr: false,
		},
		{
			name:    "missing repo_url",
			req:     CreateTaskRequest{Prompt: "Fix bug"},
			wantErr: true,
		},
		{
			name:    "missing prompt",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo"},
			wantErr: true,
		},
		{
			name:    "negative budget",
			req:     CreateTaskRequest{RepoURL: "https://github.com/test/repo", Prompt: "Fix", MaxBudgetUSD: -1},
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

	nonTerminal := []TaskStatus{TaskStatusPending, TaskStatusProvisioning, TaskStatusRunning, TaskStatusInterrupted}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%q should not be terminal", s)
		}
	}
}
