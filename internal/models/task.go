package models

import (
	"encoding/json"
	"fmt"
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
)

func (s TaskStatus) IsTerminal() bool {
	return s == TaskStatusCompleted || s == TaskStatusFailed || s == TaskStatusCancelled
}

type Task struct {
	ID            string            `json:"id"`
	Status        TaskStatus        `json:"status"`
	RepoURL       string            `json:"repo_url"`
	Branch        string            `json:"branch"`
	TargetBranch  string            `json:"target_branch"`
	Prompt        string            `json:"prompt"`
	Context       string            `json:"context,omitempty"`
	Model         string            `json:"model,omitempty"`
	Effort        string            `json:"effort,omitempty"`
	MaxBudgetUSD  float64           `json:"max_budget_usd,omitempty"`
	MaxRuntimeMin int               `json:"max_runtime_min,omitempty"`
	MaxTurns      int               `json:"max_turns,omitempty"`
	CreatePR      bool              `json:"create_pr"`
	SelfReview    bool              `json:"self_review"`
	PRTitle       string            `json:"pr_title,omitempty"`
	PRBody        string            `json:"pr_body,omitempty"`
	PRURL         string            `json:"pr_url,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
	ClaudeMD      string            `json:"claude_md,omitempty"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
	InstanceID    string            `json:"instance_id,omitempty"`
	ContainerID   string            `json:"container_id,omitempty"`
	RetryCount    int               `json:"retry_count"`
	CostUSD       float64           `json:"cost_usd,omitempty"`
	Error         string            `json:"error,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
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
type CreateTaskRequest struct {
	RepoURL       string            `json:"repo_url"`
	Branch        string            `json:"branch,omitempty"`
	TargetBranch  string            `json:"target_branch,omitempty"`
	Prompt        string            `json:"prompt"`
	Context       string            `json:"context,omitempty"`
	Model         string            `json:"model,omitempty"`
	Effort        string            `json:"effort,omitempty"`
	MaxBudgetUSD  float64           `json:"max_budget_usd,omitempty"`
	MaxRuntimeMin int               `json:"max_runtime_min,omitempty"`
	MaxTurns      int               `json:"max_turns,omitempty"`
	CreatePR      bool              `json:"create_pr"`
	SelfReview    bool              `json:"self_review"`
	PRTitle       string            `json:"pr_title,omitempty"`
	PRBody        string            `json:"pr_body,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
	ClaudeMD      string            `json:"claude_md,omitempty"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
}

func (r *CreateTaskRequest) Validate() error {
	if r.RepoURL == "" {
		return fmt.Errorf("repo_url is required")
	}
	if r.Prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	if r.MaxBudgetUSD < 0 {
		return fmt.Errorf("max_budget_usd must be non-negative")
	}
	if r.MaxRuntimeMin < 0 {
		return fmt.Errorf("max_runtime_min must be non-negative")
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
