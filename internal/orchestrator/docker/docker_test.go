package docker

import (
	"strings"
	"testing"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/orchestrator"
)

// Compile-time check: *Manager must satisfy orchestrator.Runner.
var _ orchestrator.Runner = (*Manager)(nil)

func TestEnvFlag(t *testing.T) {
	got := envFlag("FOO", "bar")
	want := "-e FOO=bar"
	if got != want {
		t.Errorf("envFlag(\"FOO\", \"bar\") = %q, want %q", got, want)
	}
}

func TestParseInspectOutput(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantDone bool
		wantCode int
		wantErr  string
		wantLog  string
		wantFail bool
	}{
		{
			name:     "running container",
			output:   "running 0\nsome log output",
			wantDone: false,
			wantLog:  "some log output",
		},
		{
			name:     "exited success",
			output:   "exited 0\nfinal logs here",
			wantDone: true,
			wantCode: 0,
			wantLog:  "final logs here",
		},
		{
			name:     "exited with error",
			output:   "exited 1\nerror log",
			wantDone: true,
			wantCode: 1,
			wantErr:  "container exited with code 1",
			wantLog:  "error log",
		},
		{
			name:     "dead container",
			output:   "dead 137\nOOM killed",
			wantDone: true,
			wantCode: 137,
			wantErr:  "container exited with code 137",
			wantLog:  "OOM killed",
		},
		{
			name:     "no log tail",
			output:   "running 0",
			wantDone: false,
		},
		{
			name:     "empty output",
			output:   "",
			wantFail: true,
		},
		{
			name:     "malformed single field",
			output:   "running",
			wantFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := parseInspectOutput(tt.output)
			if tt.wantFail {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if status.Done != tt.wantDone {
				t.Errorf("Done = %v, want %v", status.Done, tt.wantDone)
			}
			if status.ExitCode != tt.wantCode {
				t.Errorf("ExitCode = %d, want %d", status.ExitCode, tt.wantCode)
			}
			if status.Error != tt.wantErr {
				t.Errorf("Error = %q, want %q", status.Error, tt.wantErr)
			}
			if status.LogTail != tt.wantLog {
				t.Errorf("LogTail = %q, want %q", status.LogTail, tt.wantLog)
			}
		})
	}
}

func TestBuildEnvFlags(t *testing.T) {
	cfg := &config.Config{
		AuthMode:        config.AuthModeAPIKey,
		AnthropicAPIKey: "sk-test-key",
		GitHubToken:     "ghp_testtoken",
	}
	dm := NewManager(cfg)

	task := &models.Task{
		ID:           "bf_01ABC",
		RepoURL:      "https://github.com/test/repo",
		Branch:       "feature-branch",
		TargetBranch: "main",
		Prompt:       "Fix the bug",
		Model:        "claude-sonnet-4-6",
		Effort:       "high",
		MaxBudgetUSD: 5.0,
		MaxTurns:     100,
		CreatePR:     true,
		SelfReview:   false,
		PRTitle:      "Fix bug",
		ClaudeMD:     "# Instructions",
		EnvVars:      map[string]string{"CUSTOM_VAR": "custom_value"},
	}

	flags := dm.buildEnvFlags(task)
	joined := strings.Join(flags, " ")

	mustContain := []string{
		"-e TASK_ID=bf_01ABC",
		"-e REPO_URL=",
		"-e BRANCH=",
		"-e PROMPT=",
		"-e MODEL=",
		"-e EFFORT=",
		"-e MAX_BUDGET_USD=5",
		"-e MAX_TURNS=100",
		"-e CREATE_PR=true",
		"-e SELF_REVIEW=false",
		"-e AUTH_MODE=api_key",
		"-e ANTHROPIC_API_KEY=sk-test-key",
		"-e GITHUB_TOKEN=ghp_testtoken",
		"-e PR_TITLE=",
		"-e CLAUDE_MD=",
		"-e CUSTOM_VAR=",
	}
	for _, s := range mustContain {
		if !strings.Contains(joined, s) {
			t.Errorf("flags missing %q\ngot: %s", s, joined)
		}
	}

	if strings.Contains(joined, "PR_BODY") {
		t.Error("PR_BODY should not be set when empty")
	}
	if strings.Contains(joined, "TASK_CONTEXT") {
		t.Error("TASK_CONTEXT should not be set when empty")
	}
}

func TestBuildEnvFlags_MaxSubscription(t *testing.T) {
	cfg := &config.Config{
		AuthMode: config.AuthModeMaxSubscription,
	}
	dm := NewManager(cfg)

	task := &models.Task{
		ID:      "bf_01ABC",
		RepoURL: "https://github.com/test/repo",
		Prompt:  "Do something",
	}

	flags := dm.buildEnvFlags(task)
	joined := strings.Join(flags, " ")

	if strings.Contains(joined, "ANTHROPIC_API_KEY") {
		t.Error("ANTHROPIC_API_KEY should not be set in max_subscription mode")
	}
	if !strings.Contains(joined, "-e AUTH_MODE=max_subscription") {
		t.Error("AUTH_MODE should be max_subscription")
	}
}

func TestBuildVolumeFlags(t *testing.T) {
	tests := []struct {
		name     string
		authMode config.AuthMode
		credPath string
		want     string
	}{
		{
			name:     "api_key mode",
			authMode: config.AuthModeAPIKey,
			want:     "",
		},
		{
			name:     "max_subscription with path",
			authMode: config.AuthModeMaxSubscription,
			credPath: "/home/user/.claude",
			want:     "-v /home/user/.claude:/home/agent/.claude:ro",
		},
		{
			name:     "max_subscription without path",
			authMode: config.AuthModeMaxSubscription,
			credPath: "",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AuthMode:              tt.authMode,
				ClaudeCredentialsPath: tt.credPath,
			}
			dm := NewManager(cfg)
			got := dm.buildVolumeFlags()
			if got != tt.want {
				t.Errorf("buildVolumeFlags() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildRunCommand(t *testing.T) {
	cfg := &config.Config{
		AuthMode:        config.AuthModeAPIKey,
		AnthropicAPIKey: "sk-test",
		ContainerCPUs:   2,
		ContainerMemGB:  8,
		AgentImage:      "backflow-agent",
	}
	dm := NewManager(cfg)

	task := &models.Task{
		ID:      "bf_01ABC",
		RepoURL: "https://github.com/test/repo",
		Prompt:  "Fix bug",
	}

	cmd := dm.buildRunCommand(task)

	if !strings.HasPrefix(cmd, "docker run -d --cpus=2 --memory=8g") {
		t.Errorf("unexpected command prefix: %s", cmd)
	}
	if !strings.HasSuffix(cmd, "backflow-agent") {
		t.Errorf("command should end with image name, got: %s", cmd)
	}
	if !strings.Contains(cmd, "-e TASK_ID=bf_01ABC") {
		t.Error("command missing TASK_ID")
	}
}

func TestBuildRunCommand_CustomImage(t *testing.T) {
	cfg := &config.Config{
		AuthMode:        config.AuthModeAPIKey,
		AnthropicAPIKey: "sk-test",
		ContainerCPUs:   4,
		ContainerMemGB:  16,
		AgentImage:      "my-custom-agent:v2",
	}
	dm := NewManager(cfg)

	task := &models.Task{
		ID:      "bf_01XYZ",
		RepoURL: "https://github.com/test/repo",
		Prompt:  "Do something",
	}

	cmd := dm.buildRunCommand(task)

	if !strings.HasSuffix(cmd, "my-custom-agent:v2") {
		t.Errorf("command should end with custom image name, got: %s", cmd)
	}
}

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"", "''"},
		{"it's a test", "'it'\"'\"'s a test'"},
		{"no special chars", "'no special chars'"},
		{"multi'quote'test", "'multi'\"'\"'quote'\"'\"'test'"},
		{"spaces and\ttabs", "'spaces and\ttabs'"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shellEscape(tt.input)
			if got != tt.want {
				t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsHexString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid lowercase", "abcdef0123456789", true},
		{"valid uppercase", "ABCDEF0123456789", true},
		{"valid mixed", "aAbBcC123", true},
		{"valid short", "a", true},
		{"empty string", "", false},
		{"contains g", "abcdefg", false},
		{"contains space", "abc def", false},
		{"contains dash", "abc-def", false},
		{"typical container id", "d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHexString(tt.input)
			if got != tt.want {
				t.Errorf("isHexString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
