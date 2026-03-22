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
			name:    "valid review mode with pr url",
			req:     CreateTaskRequest{TaskMode: "review", ReviewPRURL: "https://github.com/test/repo/pull/42"},
			wantErr: false,
		},
		{
			name:    "valid review mode with pr url and prompt",
			req:     CreateTaskRequest{TaskMode: "review", ReviewPRURL: "https://github.com/test/repo/pull/10", Prompt: "Focus on security"},
			wantErr: false,
		},
		{
			name:    "valid review mode backward compat with repo_url and pr number",
			req:     CreateTaskRequest{TaskMode: "review", RepoURL: "https://github.com/test/repo", ReviewPRNumber: 42},
			wantErr: false,
		},
		{
			name:    "review mode missing pr url and pr number",
			req:     CreateTaskRequest{TaskMode: "review", RepoURL: "https://github.com/test/repo"},
			wantErr: true,
		},
		{
			name:    "review mode invalid pr url",
			req:     CreateTaskRequest{TaskMode: "review", ReviewPRURL: "https://github.com/test/repo"},
			wantErr: true,
		},
		{
			name:    "review mode pr url with trailing path",
			req:     CreateTaskRequest{TaskMode: "review", ReviewPRURL: "https://github.com/test/repo/pull/42/files"},
			wantErr: false,
		},
		{
			name:    "review mode pr url with repo_url is conflict",
			req:     CreateTaskRequest{TaskMode: "review", ReviewPRURL: "https://github.com/test/repo/pull/42", RepoURL: "https://github.com/test/repo"},
			wantErr: true,
		},
		{
			name:    "review mode pr url with pr number is conflict",
			req:     CreateTaskRequest{TaskMode: "review", ReviewPRURL: "https://github.com/test/repo/pull/42", ReviewPRNumber: 42},
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

func TestParsePullRequestURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantRepo   string
		wantNumber int
		wantErr    bool
	}{
		{
			name:       "standard PR URL",
			url:        "https://github.com/owner/repo/pull/123",
			wantRepo:   "https://github.com/owner/repo",
			wantNumber: 123,
		},
		{
			name:       "PR URL with trailing path",
			url:        "https://github.com/owner/repo/pull/42/files",
			wantRepo:   "https://github.com/owner/repo",
			wantNumber: 42,
		},
		{
			name:    "missing pull segment",
			url:     "https://github.com/owner/repo/issues/5",
			wantErr: true,
		},
		{
			name:    "non-numeric PR number",
			url:     "https://github.com/owner/repo/pull/abc",
			wantErr: true,
		},
		{
			name:    "too short path",
			url:     "https://github.com/owner",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, number, err := ParsePullRequestURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePullRequestURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if repo != tt.wantRepo {
					t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
				}
				if number != tt.wantNumber {
					t.Errorf("number = %d, want %d", number, tt.wantNumber)
				}
			}
		})
	}
}

func TestValidateReviewDeriveFields(t *testing.T) {
	// Validate should populate RepoURL and ReviewPRNumber from ReviewPRURL
	req := CreateTaskRequest{
		TaskMode:    "review",
		ReviewPRURL: "https://github.com/test/repo/pull/99",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
	if req.RepoURL != "https://github.com/test/repo" {
		t.Errorf("RepoURL = %q, want %q", req.RepoURL, "https://github.com/test/repo")
	}
	if req.ReviewPRNumber != 99 {
		t.Errorf("ReviewPRNumber = %d, want 99", req.ReviewPRNumber)
	}
}

func TestValidateReviewBackwardCompatBuildsPRURL(t *testing.T) {
	// Backward compat: repo_url + review_pr_number should derive review_pr_url
	req := CreateTaskRequest{
		TaskMode:       "review",
		RepoURL:        "https://github.com/test/repo",
		ReviewPRNumber: 7,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
	if req.ReviewPRURL != "https://github.com/test/repo/pull/7" {
		t.Errorf("ReviewPRURL = %q, want %q", req.ReviewPRURL, "https://github.com/test/repo/pull/7")
	}
}

func TestFindFirstURL(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "plain PR URL",
			text: "Review the PR: https://github.com/owner/repo/pull/123",
			want: "https://github.com/owner/repo/pull/123",
		},
		{
			name: "PR URL in parentheses",
			text: "Review this (https://github.com/owner/repo/pull/42)",
			want: "https://github.com/owner/repo/pull/42",
		},
		{
			name: "PR URL with trailing path",
			text: "Look at https://github.com/owner/repo/pull/7/files please",
			want: "https://github.com/owner/repo/pull/7/files",
		},
		{
			name: "no URL",
			text: "Fix the bug in the login page",
			want: "",
		},
		{
			name: "non-PR URL is returned too",
			text: "See https://github.com/owner/repo/issues/5",
			want: "https://github.com/owner/repo/issues/5",
		},
		{
			name: "PR URL at start",
			text: "https://github.com/org/project/pull/99 needs review",
			want: "https://github.com/org/project/pull/99",
		},
		{
			name: "multiple URLs returns first",
			text: "Compare https://github.com/a/b/pull/1 with https://github.com/a/b/pull/2",
			want: "https://github.com/a/b/pull/1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindFirstURL(tt.text)
			if got != tt.want {
				t.Errorf("FindFirstURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateAutoDetectsReviewMode(t *testing.T) {
	t.Run("PR URL in prompt auto-detects review mode", func(t *testing.T) {
		req := CreateTaskRequest{
			Prompt: "Review the PR: https://github.com/test/repo/pull/115",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
		if req.TaskMode != TaskModeReview {
			t.Errorf("TaskMode = %q, want %q", req.TaskMode, TaskModeReview)
		}
		if req.ReviewPRURL != "https://github.com/test/repo/pull/115" {
			t.Errorf("ReviewPRURL = %q", req.ReviewPRURL)
		}
		if req.RepoURL != "https://github.com/test/repo" {
			t.Errorf("RepoURL = %q", req.RepoURL)
		}
		if req.ReviewPRNumber != 115 {
			t.Errorf("ReviewPRNumber = %d, want 115", req.ReviewPRNumber)
		}
	})

	t.Run("PR URL with trailing /files auto-detects", func(t *testing.T) {
		req := CreateTaskRequest{
			Prompt: "https://github.com/owner/repo/pull/7/files needs review",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
		if req.TaskMode != TaskModeReview {
			t.Errorf("TaskMode = %q, want %q", req.TaskMode, TaskModeReview)
		}
		if req.ReviewPRNumber != 7 {
			t.Errorf("ReviewPRNumber = %d, want 7", req.ReviewPRNumber)
		}
	})

	t.Run("non-PR URL does not trigger auto-detect", func(t *testing.T) {
		req := CreateTaskRequest{
			RepoURL: "https://github.com/test/repo",
			Prompt:  "Fix the issue https://github.com/test/repo/issues/5",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
		if req.TaskMode != "" {
			t.Errorf("TaskMode = %q, want empty (code default)", req.TaskMode)
		}
	})

	t.Run("no URL in prompt does not trigger auto-detect", func(t *testing.T) {
		req := CreateTaskRequest{
			RepoURL: "https://github.com/test/repo",
			Prompt:  "review the latest changes on main",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
		if req.TaskMode != "" {
			t.Errorf("TaskMode = %q, want empty (code default)", req.TaskMode)
		}
	})

	t.Run("explicit task_mode=code is not overridden", func(t *testing.T) {
		req := CreateTaskRequest{
			TaskMode: TaskModeCode,
			RepoURL:  "https://github.com/test/repo",
			Prompt:   "Review https://github.com/test/repo/pull/5 and fix the issues",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() unexpected error: %v", err)
		}
		if req.TaskMode != TaskModeCode {
			t.Errorf("TaskMode = %q, want %q", req.TaskMode, TaskModeCode)
		}
		if req.ReviewPRURL != "" {
			t.Errorf("ReviewPRURL should be empty, got %q", req.ReviewPRURL)
		}
	})
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
