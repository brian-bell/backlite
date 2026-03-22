package models

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
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
	ReviewPRURL     string            `json:"review_pr_url,omitempty"`
	ReviewPRNumber  int               `json:"review_pr_number,omitempty"`
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
type CreateTaskRequest struct {
	TaskMode        string            `json:"task_mode,omitempty"`
	Harness         string            `json:"harness,omitempty"`
	RepoURL         string            `json:"repo_url"`
	Branch          string            `json:"branch,omitempty"`
	TargetBranch    string            `json:"target_branch,omitempty"`
	ReviewPRURL     string            `json:"review_pr_url,omitempty"`
	ReviewPRNumber  int               `json:"review_pr_number,omitempty"`
	Prompt          string            `json:"prompt,omitempty"`
	Context         string            `json:"context,omitempty"`
	Model           string            `json:"model,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	MaxBudgetUSD    float64           `json:"max_budget_usd,omitempty"`
	MaxRuntimeMin   int               `json:"max_runtime_min,omitempty"`
	MaxTurns        int               `json:"max_turns,omitempty"`
	CreatePR        bool              `json:"create_pr"`
	SelfReview      bool              `json:"self_review"`
	SaveAgentOutput *bool             `json:"save_agent_output,omitempty"`
	PRTitle         string            `json:"pr_title,omitempty"`
	PRBody          string            `json:"pr_body,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	ClaudeMD        string            `json:"claude_md,omitempty"`
	EnvVars         map[string]string `json:"env_vars,omitempty"`
}

// ParsePullRequestURL extracts the repository URL and PR number from a GitHub PR URL.
// It accepts URLs like https://github.com/owner/repo/pull/123 (with optional trailing path).
func ParsePullRequestURL(prURL string) (repoURL string, prNumber int, err error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return "", 0, fmt.Errorf("invalid PR URL: %w", err)
	}
	if u.Host == "" || u.Path == "" {
		return "", 0, fmt.Errorf("invalid PR URL: missing host or path")
	}

	// Path looks like /owner/repo/pull/123 or /owner/repo/pull/123/files
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", 0, fmt.Errorf("invalid PR URL: expected format https://host/owner/repo/pull/NUMBER")
	}

	prNumber, err = strconv.Atoi(parts[3])
	if err != nil || prNumber <= 0 {
		return "", 0, fmt.Errorf("invalid PR URL: PR number must be a positive integer")
	}

	repoURL = fmt.Sprintf("%s://%s/%s/%s", u.Scheme, u.Host, parts[0], parts[1])
	return repoURL, prNumber, nil
}

var urlPattern = regexp.MustCompile(`https?://\S+`)

// FindFirstURL extracts the first URL from text, stripping trailing punctuation.
func FindFirstURL(text string) string {
	match := urlPattern.FindString(text)
	if match == "" {
		return ""
	}
	// Strip trailing punctuation that is likely not part of the URL.
	match = strings.TrimRight(match, ")>,.'\"")
	return match
}

// ReviewInference holds the fields parsed from a PR URL found in a prompt.
type ReviewInference struct {
	PRURL    string
	RepoURL  string
	PRNumber int
}

// InferReviewMode checks whether a prompt's first URL is a GitHub PR URL.
// Returns nil if no PR URL is found.
func InferReviewMode(prompt string) *ReviewInference {
	firstURL := FindFirstURL(prompt)
	if firstURL == "" {
		return nil
	}
	repo, num, err := ParsePullRequestURL(firstURL)
	if err != nil {
		return nil
	}
	return &ReviewInference{PRURL: firstURL, RepoURL: repo, PRNumber: num}
}

func (r *CreateTaskRequest) Validate() error {
	// Validate task_mode
	switch r.TaskMode {
	case "", TaskModeCode:
		// Auto-detect: if task_mode is unset and the first URL is a PR,
		// switch to review mode with all fields populated directly.
		if r.TaskMode == "" {
			if inf := InferReviewMode(r.Prompt); inf != nil {
				r.TaskMode = TaskModeReview
				r.ReviewPRURL = inf.PRURL
				r.RepoURL = inf.RepoURL
				r.ReviewPRNumber = inf.PRNumber
				break
			}
		}
		// In code mode, repo_url and prompt are required
		if r.RepoURL == "" {
			return fmt.Errorf("repo_url is required")
		}
		if r.Prompt == "" {
			return fmt.Errorf("prompt is required")
		}
	case TaskModeReview:
		if r.ReviewPRURL != "" {
			if r.RepoURL != "" || r.ReviewPRNumber > 0 {
				return fmt.Errorf("review_pr_url cannot be combined with repo_url or review_pr_number; use one or the other")
			}
			// Parse the PR URL to derive repo_url and review_pr_number
			repoURL, prNumber, err := ParsePullRequestURL(r.ReviewPRURL)
			if err != nil {
				return err
			}
			r.RepoURL = repoURL
			r.ReviewPRNumber = prNumber
		} else if r.RepoURL != "" && r.ReviewPRNumber > 0 {
			// Backward compat: construct the PR URL from repo_url + review_pr_number
			r.ReviewPRURL = fmt.Sprintf("%s/pull/%d", strings.TrimRight(r.RepoURL, "/"), r.ReviewPRNumber)
		} else {
			return fmt.Errorf("review_pr_url is required for review mode (e.g. https://github.com/owner/repo/pull/123)")
		}
	default:
		return fmt.Errorf("task_mode must be 'code' or 'review'")
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
