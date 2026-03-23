package models

import (
	"encoding/json"
	"fmt"
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
	RepoURL         string            `json:"repo_url"`
	Branch          string            `json:"branch"`
	TargetBranch    string            `json:"target_branch"`
	Prompt          string            `json:"prompt"`
	Context         string            `json:"context,omitempty"`
	Model           string            `json:"model,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	MaxBudgetUSD    float64           `json:"max_budget_usd,omitempty"`
	MaxRuntimeMin   int               `json:"max_runtime_min,omitempty"`
	MaxTurns        int               `json:"max_turns,omitempty"`
	CreatePR        bool              `json:"create_pr"`
	SelfReview      bool              `json:"self_review"`
	PRTitle         string            `json:"pr_title,omitempty"`
	PRBody          string            `json:"pr_body,omitempty"`
	PRURL           string            `json:"pr_url,omitempty"`
	SaveAgentOutput bool              `json:"save_agent_output"`
	OutputURL       string            `json:"output_url,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	ClaudeMD        string            `json:"claude_md,omitempty"`
	EnvVars         map[string]string `json:"env_vars,omitempty"`
	InstanceID      string            `json:"instance_id,omitempty"`
	ContainerID     string            `json:"container_id,omitempty"`
	RetryCount      int               `json:"retry_count"`
	CostUSD         float64           `json:"cost_usd,omitempty"`
	ElapsedTimeSec  int               `json:"elapsed_time_sec,omitempty"`
	ReplyChannel    string            `json:"reply_channel,omitempty"`
	Error           string            `json:"error,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	CompletedAt     *time.Time        `json:"completed_at,omitempty"`
}

// RedactReplyChannel replaces the full reply channel (e.g. "sms:+15551234567")
// with just the channel type (e.g. "sms") to avoid exposing phone numbers in
// API responses.
func (t *Task) RedactReplyChannel() {
	if t.ReplyChannel == "" {
		return
	}
	if idx := strings.Index(t.ReplyChannel, ":"); idx >= 0 {
		t.ReplyChannel = t.ReplyChannel[:idx]
	}
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
	Harness         string            `json:"harness,omitempty"`
	Context         string            `json:"context,omitempty"`
	Model           string            `json:"model,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	MaxBudgetUSD    float64           `json:"max_budget_usd,omitempty"`
	MaxRuntimeMin   int               `json:"max_runtime_min,omitempty"`
	MaxTurns        int               `json:"max_turns,omitempty"`
	CreatePR        *bool             `json:"create_pr,omitempty"`
	SelfReview      *bool             `json:"self_review,omitempty"`
	SaveAgentOutput *bool             `json:"save_agent_output,omitempty"`
	PRTitle         string            `json:"pr_title,omitempty"`
	PRBody          string            `json:"pr_body,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	ClaudeMD        string            `json:"claude_md,omitempty"`
	EnvVars         map[string]string `json:"env_vars,omitempty"`
}

func (r *CreateTaskRequest) Validate() error {
	if r.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if r.MaxBudgetUSD < 0 {
		return fmt.Errorf("max_budget_usd must be non-negative")
	}
	if r.MaxRuntimeMin < 0 {
		return fmt.Errorf("max_runtime_min must be non-negative")
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
	return nil
}
