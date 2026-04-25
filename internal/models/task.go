package models

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

type TaskStatus string

const (
	TaskStatusPending      TaskStatus = "pending"
	TaskStatusProvisioning TaskStatus = "provisioning"
	TaskStatusRunning      TaskStatus = "running"
	TaskStatusCompleted    TaskStatus = "completed"
	TaskStatusFailed       TaskStatus = "failed"
	TaskStatusInterrupted  TaskStatus = "interrupted"
	TaskStatusCancelled    TaskStatus = "cancelled"
	TaskStatusRecovering   TaskStatus = "recovering"
)

func (s TaskStatus) IsTerminal() bool {
	return s == TaskStatusCompleted || s == TaskStatusFailed || s == TaskStatusCancelled
}

// TaskMode controls how the agent container behaves.
const (
	TaskModeAuto   = "auto"   // Prep stage infers code or review from the prompt
	TaskModeCode   = "code"   // Default: clone, code, commit, push, optionally create PR
	TaskModeReview = "review" // Review an existing PR and post feedback as comments
	TaskModeRead   = "read"   // Fetch a URL, summarize it, and store the reading
)

type Harness string

const (
	HarnessClaudeCode Harness = "claude_code"
	HarnessCodex      Harness = "codex"
)

type Task struct {
	ID              string            `json:"id"`
	Status          TaskStatus        `json:"status"`
	TaskMode        string            `json:"task_mode"`
	Harness         Harness           `json:"harness"`
	RepoURL         string            `json:"repo_url,omitempty"`
	Branch          string            `json:"branch,omitempty"`
	TargetBranch    string            `json:"target_branch,omitempty"`
	Prompt          string            `json:"prompt,omitempty"`
	Context         string            `json:"context,omitempty"`
	Model           string            `json:"model,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	AgentImage      string            `json:"agent_image,omitempty"`
	Force           bool              `json:"force,omitempty"`
	MaxBudgetUSD    float64           `json:"max_budget_usd,omitempty"`
	MaxRuntimeSec   int               `json:"max_runtime_sec,omitempty"`
	MaxTurns        int               `json:"max_turns,omitempty"`
	CreatePR        bool              `json:"create_pr"`
	SelfReview      bool              `json:"self_review"`
	ParentTaskID    *string           `json:"parent_task_id,omitempty"`
	PRTitle         string            `json:"pr_title,omitempty"`
	PRBody          string            `json:"pr_body,omitempty"`
	PRURL           string            `json:"pr_url,omitempty"`
	SaveAgentOutput bool              `json:"save_agent_output"`
	OutputURL       string            `json:"output_url,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	ClaudeMD        string            `json:"claude_md,omitempty"`
	EnvVars         map[string]string `json:"env_vars,omitempty"`
	ContainerID     string            `json:"container_id,omitempty"`
	RetryCount      int               `json:"retry_count"`
	UserRetryCount  int               `json:"user_retry_count"`
	ReadyForRetry   bool              `json:"ready_for_retry"`
	CostUSD         float64           `json:"cost_usd,omitempty"`
	ElapsedTimeSec  int               `json:"elapsed_time_sec,omitempty"`
	Error           string            `json:"error,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
}

// AllowedToolsJSON returns the JSON representation for DB storage.
func (t *Task) AllowedToolsJSON() string {
	if len(t.AllowedTools) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(t.AllowedTools)
	return string(b)
}

// EnvVarsJSON returns the JSON representation for DB storage.
func (t *Task) EnvVarsJSON() string {
	if len(t.EnvVars) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(t.EnvVars)
	return string(b)
}

// CreateTaskRequest is the API input for creating a task.
// Prompt is the only required field — the agent container's Prep stage
// infers repo_url, target_branch, and task_mode from the prompt.
type CreateTaskRequest struct {
	Prompt          string            `json:"prompt"`
	TaskMode        *string           `json:"task_mode,omitempty"`
	Harness         string            `json:"harness,omitempty"`
	Context         string            `json:"context,omitempty"`
	Model           string            `json:"model,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	MaxBudgetUSD    float64           `json:"max_budget_usd,omitempty"`
	MaxRuntimeSec   int               `json:"max_runtime_sec,omitempty"`
	MaxTurns        int               `json:"max_turns,omitempty"`
	CreatePR        *bool             `json:"create_pr,omitempty"`
	SelfReview      *bool             `json:"self_review,omitempty"`
	SaveAgentOutput *bool             `json:"save_agent_output,omitempty"`
	Force           *bool             `json:"force,omitempty"`
	PRTitle         string            `json:"pr_title,omitempty"`
	PRBody          string            `json:"pr_body,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	ClaudeMD        string            `json:"claude_md,omitempty"`
	EnvVars         map[string]string `json:"env_vars,omitempty"`
}

// validEnvVarKey matches POSIX environment variable names: must start with a
// letter or underscore, followed by letters, digits, or underscores.
var validEnvVarKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedEnvVarKeys are system env var names set by the Docker and Fargate
// runners. User-supplied env vars must not override these.
var reservedEnvVarKeys = map[string]bool{
	"TASK_ID":               true,
	"TASK_MODE":             true,
	"HARNESS":               true,
	"REPO_URL":              true,
	"BRANCH":                true,
	"TARGET_BRANCH":         true,
	"PROMPT":                true,
	"MODEL":                 true,
	"EFFORT":                true,
	"MAX_BUDGET_USD":        true,
	"MAX_TURNS":             true,
	"CREATE_PR":             true,
	"SELF_REVIEW":           true,
	"FORCE":                 true,
	"PR_TITLE":              true,
	"PR_BODY":               true,
	"CLAUDE_MD":             true,
	"TASK_CONTEXT":          true,
	"BACKFLOW_API_KEY":      true,
	"BACKFLOW_API_BASE_URL": true,
	"ANTHROPIC_API_KEY":     true,
	"OPENAI_API_KEY":        true,
	"GITHUB_TOKEN":          true,
}

// containsNullByte returns true if s contains a null byte, which PostgreSQL
// text columns reject.
func containsNullByte(s string) bool {
	return strings.ContainsRune(s, 0)
}

func (r *CreateTaskRequest) Validate() error {
	if r.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	for _, s := range []string{r.Prompt, r.Context, r.ClaudeMD, r.PRTitle, r.PRBody, r.Model, r.Harness, r.Effort} {
		if containsNullByte(s) {
			return fmt.Errorf("request contains invalid null bytes")
		}
	}
	for _, s := range r.AllowedTools {
		if containsNullByte(s) {
			return fmt.Errorf("request contains invalid null bytes")
		}
	}
	for k, v := range r.EnvVars {
		if !validEnvVarKey.MatchString(k) {
			return fmt.Errorf("invalid env var key %q: must match [A-Za-z_][A-Za-z0-9_]*", k)
		}
		if reservedEnvVarKeys[k] {
			return fmt.Errorf("env var key %q is reserved and cannot be overridden", k)
		}
		if containsNullByte(v) {
			return fmt.Errorf("request contains invalid null bytes")
		}
	}
	if r.MaxBudgetUSD < 0 {
		return fmt.Errorf("max_budget_usd must be non-negative")
	}
	if r.MaxRuntimeSec < 0 {
		return fmt.Errorf("max_runtime_sec must be non-negative")
	}
	if r.MaxRuntimeSec > math.MaxInt32 {
		return fmt.Errorf("max_runtime_sec exceeds maximum allowed value")
	}
	if r.MaxTurns < 0 {
		return fmt.Errorf("max_turns must be non-negative")
	}
	if r.MaxTurns > math.MaxInt32 {
		return fmt.Errorf("max_turns exceeds maximum allowed value")
	}
	if r.Harness != "" {
		switch Harness(r.Harness) {
		case HarnessClaudeCode, HarnessCodex:
		default:
			return fmt.Errorf("harness must be claude_code or codex")
		}
	}
	if r.Effort != "" {
		switch r.Effort {
		case "low", "medium", "high", "xhigh":
		default:
			return fmt.Errorf("effort must be low, medium, high, or xhigh")
		}
	}
	if r.TaskMode != nil {
		switch *r.TaskMode {
		case "", TaskModeAuto, TaskModeRead:
		default:
			return fmt.Errorf("task_mode must be auto or read (code and review are inferred from the prompt)")
		}
	}
	return nil
}
