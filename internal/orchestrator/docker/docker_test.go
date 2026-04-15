package docker

import (
	"os"
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
		"-e PR_TITLE=",
		"-e CLAUDE_MD=",
		"-e 'CUSTOM_VAR'=",
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

	// Secrets must not appear in env flags — they go via --env-file.
	for _, secret := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GITHUB_TOKEN"} {
		if strings.Contains(joined, secret) {
			t.Errorf("secret %q must not appear in env flags", secret)
		}
	}
}

func TestBuildEnvFlagsForceFlag(t *testing.T) {
	dm := NewManager(&config.Config{})
	for _, force := range []bool{true, false} {
		task := &models.Task{ID: "bf_x", Prompt: "p", Force: force}
		joined := strings.Join(dm.buildEnvFlags(task), " ")
		want := "-e FORCE=" + map[bool]string{true: "true", false: "false"}[force]
		if !strings.Contains(joined, want) {
			t.Errorf("force=%v: flags missing %q\ngot: %s", force, want, joined)
		}
	}
}

func TestBuildRunCommand(t *testing.T) {
	cfg := &config.Config{
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

	cmd := dm.buildRunCommand(task, "")

	if !strings.HasPrefix(cmd, "docker run -d --cpus=2 --memory=8g") {
		t.Errorf("unexpected command prefix: %s", cmd)
	}
	if !strings.HasSuffix(cmd, "backflow-agent") {
		t.Errorf("command should end with image name, got: %s", cmd)
	}
	if !strings.Contains(cmd, "-e TASK_ID=bf_01ABC") {
		t.Error("command missing TASK_ID")
	}
	if strings.Contains(cmd, "--env-file") {
		t.Error("--env-file should not appear when envFilePath is empty")
	}
}

func TestBuildRunCommand_WithEnvFile(t *testing.T) {
	cfg := &config.Config{
		ContainerCPUs:  2,
		ContainerMemGB: 8,
		AgentImage:     "backflow-agent",
	}
	dm := NewManager(cfg)

	task := &models.Task{
		ID:      "bf_01ABC",
		RepoURL: "https://github.com/test/repo",
		Prompt:  "Fix bug",
	}

	cmd := dm.buildRunCommand(task, "/tmp/backflow-env-12345")

	if !strings.Contains(cmd, "--env-file /tmp/backflow-env-12345") {
		t.Errorf("command should contain --env-file flag, got: %s", cmd)
	}
	if !strings.HasSuffix(cmd, "backflow-agent") {
		t.Errorf("command should end with image name, got: %s", cmd)
	}
}

func TestBuildRunCommand_CustomImage(t *testing.T) {
	cfg := &config.Config{
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

	cmd := dm.buildRunCommand(task, "")

	if !strings.HasSuffix(cmd, "my-custom-agent:v2") {
		t.Errorf("command should end with custom image name, got: %s", cmd)
	}
}

func TestBuildEnvFlags_ReadModeIncludesSupabase(t *testing.T) {
	cfg := &config.Config{
		SupabaseURL:     "https://test.supabase.co",
		SupabaseAnonKey: "sb_publishable_test",
	}
	dm := NewManager(cfg)
	task := &models.Task{ID: "bf_01ABC", TaskMode: models.TaskModeRead}

	joined := strings.Join(dm.buildEnvFlags(task), " ")

	if !strings.Contains(joined, "-e SUPABASE_URL='https://test.supabase.co'") {
		t.Errorf("flags should include SUPABASE_URL, got: %s", joined)
	}
	if !strings.Contains(joined, "-e SUPABASE_ANON_KEY='sb_publishable_test'") {
		t.Errorf("flags should include SUPABASE_ANON_KEY, got: %s", joined)
	}
}

func TestBuildEnvFlags_NonReadModeOmitsSupabase(t *testing.T) {
	cfg := &config.Config{
		SupabaseURL:     "https://test.supabase.co",
		SupabaseAnonKey: "sb_publishable_test",
	}
	dm := NewManager(cfg)
	task := &models.Task{ID: "bf_01ABC", TaskMode: models.TaskModeCode}

	joined := strings.Join(dm.buildEnvFlags(task), " ")

	if strings.Contains(joined, "SUPABASE_URL") {
		t.Errorf("flags should not include SUPABASE_URL for non-read mode, got: %s", joined)
	}
	if strings.Contains(joined, "SUPABASE_ANON_KEY") {
		t.Errorf("flags should not include SUPABASE_ANON_KEY for non-read mode, got: %s", joined)
	}
}

func TestBuildEnvFlags_ReadModeMissingSupabaseConfig(t *testing.T) {
	cfg := &config.Config{}
	dm := NewManager(cfg)
	task := &models.Task{ID: "bf_01ABC", TaskMode: models.TaskModeRead}

	joined := strings.Join(dm.buildEnvFlags(task), " ")

	if strings.Contains(joined, "SUPABASE_URL") {
		t.Errorf("flags should omit SUPABASE_URL when cfg is empty, got: %s", joined)
	}
	if strings.Contains(joined, "SUPABASE_ANON_KEY") {
		t.Errorf("flags should omit SUPABASE_ANON_KEY when cfg is empty, got: %s", joined)
	}
}

func TestBuildRunCommand_UsesTaskAgentImage(t *testing.T) {
	cfg := &config.Config{
		ContainerCPUs:  2,
		ContainerMemGB: 8,
		AgentImage:     "backflow-agent",
	}
	dm := NewManager(cfg)
	task := &models.Task{
		ID:         "bf_01ABC",
		AgentImage: "backflow-reader:v1",
	}

	cmd := dm.buildRunCommand(task, "")

	if !strings.HasSuffix(cmd, "backflow-reader:v1") {
		t.Errorf("command should end with task.AgentImage, got: %s", cmd)
	}
	if strings.HasSuffix(cmd, "backflow-agent") {
		t.Errorf("command should not fall back to cfg.AgentImage when task.AgentImage is set, got: %s", cmd)
	}
}

func TestBuildRunCommand_FallsBackToConfigAgentImage(t *testing.T) {
	cfg := &config.Config{
		ContainerCPUs:  2,
		ContainerMemGB: 8,
		AgentImage:     "backflow-agent",
	}
	dm := NewManager(cfg)
	task := &models.Task{
		ID:         "bf_01ABC",
		AgentImage: "",
	}

	cmd := dm.buildRunCommand(task, "")

	if !strings.HasSuffix(cmd, "backflow-agent") {
		t.Errorf("command should fall back to cfg.AgentImage when task.AgentImage is empty, got: %s", cmd)
	}
}

func TestBuildSecretEnvPairs(t *testing.T) {
	cfg := &config.Config{
		AnthropicAPIKey: "sk-test-key",
		OpenAIAPIKey:    "sk-openai-test",
		GitHubToken:     "ghp_testtoken",
	}
	dm := NewManager(cfg)
	task := &models.Task{ID: "bf_01ABC"}

	pairs := dm.buildSecretEnvPairs(task)

	want := map[string]bool{
		"ANTHROPIC_API_KEY=sk-test-key": true,
		"OPENAI_API_KEY=sk-openai-test": true,
		"GITHUB_TOKEN=ghp_testtoken":    true,
	}
	for _, p := range pairs {
		delete(want, p)
	}
	for missing := range want {
		t.Errorf("missing secret pair: %s", missing)
	}
}

func TestBuildSecretEnvPairs_NoSecrets(t *testing.T) {
	cfg := &config.Config{}
	dm := NewManager(cfg)
	task := &models.Task{ID: "bf_01ABC"}

	pairs := dm.buildSecretEnvPairs(task)
	if len(pairs) != 0 {
		t.Errorf("expected no secret pairs, got %v", pairs)
	}
}

func TestWriteEnvFile(t *testing.T) {
	pairs := []string{"FOO=bar", "BAZ=qux with spaces"}
	path, err := writeEnvFile(pairs)
	if err != nil {
		t.Fatalf("writeEnvFile() error: %v", err)
	}
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}

	want := "FOO=bar\nBAZ=qux with spaces\n"
	if string(content) != want {
		t.Errorf("env file content = %q, want %q", string(content), want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat env file: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("env file permissions = %o, want owner-only (0600)", perm)
	}
}

func TestWriteEnvFile_Empty(t *testing.T) {
	path, err := writeEnvFile(nil)
	if err != nil {
		t.Fatalf("writeEnvFile(nil) error: %v", err)
	}
	if path != "" {
		os.Remove(path)
		t.Error("expected empty path for no secrets")
	}
}

func TestWrapWithRemoteEnvFile(t *testing.T) {
	dockerCmd := "docker run -d --env-file \"$_ef\" -e TASK_ID=bf_01 image"
	secrets := []string{"API_KEY=sk-test", "TOKEN=ghp_abc"}

	cmd := wrapWithRemoteEnvFile(dockerCmd, secrets)

	if !strings.HasPrefix(cmd, "_ef=$(mktemp) && printf") {
		t.Errorf("should start with temp file creation, got: %s", cmd)
	}
	if !strings.Contains(cmd, dockerCmd) {
		t.Error("should contain the original docker command")
	}
	if !strings.Contains(cmd, "rm -f \"$_ef\"") {
		t.Error("should clean up the temp file")
	}
	if !strings.Contains(cmd, "'API_KEY=sk-test'") {
		t.Error("should contain shell-escaped secret")
	}
}

func TestBuildEnvFlags_ShellEscapesKeys(t *testing.T) {
	cfg := &config.Config{}
	dm := NewManager(cfg)

	task := &models.Task{
		ID:      "bf_01ESC",
		RepoURL: "https://github.com/test/repo",
		Prompt:  "test",
		EnvVars: map[string]string{
			"SAFE_KEY": "safe_value",
		},
	}

	flags := dm.buildEnvFlags(task)
	joined := strings.Join(flags, " ")

	// Even valid keys should be shell-escaped (wrapped in single quotes).
	if !strings.Contains(joined, "-e 'SAFE_KEY'='safe_value'") {
		t.Errorf("expected shell-escaped key in flags, got: %s", joined)
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
