package chain

import (
	"strings"
	"testing"

	"github.com/brian-bell/backlite/internal/models"
)

// TestPlan_TableDriven covers every gate condition for chained-self-review.
func TestPlan_TableDriven(t *testing.T) {
	tests := []struct {
		name     string
		parent   models.Task
		wantNil  bool
		wantMode string
	}{
		{
			name: "happy path: code, success, self_review, has PR",
			parent: models.Task{
				ID:         "bf_PARENT0001",
				Status:     models.TaskStatusCompleted,
				TaskMode:   models.TaskModeCode,
				Harness:    models.HarnessClaudeCode,
				SelfReview: true,
				PRURL:      "https://github.com/owner/repo/pull/42",
				Prompt:     "Fix the bug",
			},
			wantMode: models.TaskModeReview,
		},
		{
			name: "happy path inherits codex harness",
			parent: models.Task{
				ID:         "bf_PARENT_X",
				Status:     models.TaskStatusCompleted,
				TaskMode:   models.TaskModeCode,
				Harness:    models.HarnessCodex,
				SelfReview: true,
				PRURL:      "https://github.com/owner/repo/pull/9",
				Prompt:     "Fix bug",
			},
			wantMode: models.TaskModeReview,
		},
		{
			name: "no chain: parent failed",
			parent: models.Task{
				ID:         "bf_FAIL00001",
				Status:     models.TaskStatusFailed,
				TaskMode:   models.TaskModeCode,
				SelfReview: true,
				PRURL:      "https://github.com/owner/repo/pull/42",
			},
			wantNil: true,
		},
		{
			name: "no chain: parent has no PR URL",
			parent: models.Task{
				ID:         "bf_NOPR00001",
				Status:     models.TaskStatusCompleted,
				TaskMode:   models.TaskModeCode,
				SelfReview: true,
				PRURL:      "",
			},
			wantNil: true,
		},
		{
			name: "no chain: self_review false",
			parent: models.Task{
				ID:         "bf_OFF000001",
				Status:     models.TaskStatusCompleted,
				TaskMode:   models.TaskModeCode,
				SelfReview: false,
				PRURL:      "https://github.com/owner/repo/pull/42",
			},
			wantNil: true,
		},
		{
			name: "no chain: parent is review mode (no nested chains)",
			parent: models.Task{
				ID:         "bf_REVIEW001",
				Status:     models.TaskStatusCompleted,
				TaskMode:   models.TaskModeReview,
				SelfReview: true,
				PRURL:      "https://github.com/owner/repo/pull/42",
			},
			wantNil: true,
		},
		{
			name: "no chain: parent is read mode",
			parent: models.Task{
				ID:         "bf_READ00001",
				Status:     models.TaskStatusCompleted,
				TaskMode:   models.TaskModeRead,
				SelfReview: true,
				PRURL:      "https://github.com/owner/repo/pull/42",
			},
			wantNil: true,
		},
		{
			name: "no chain: parent already has parent_task_id (no recursion)",
			parent: models.Task{
				ID:           "bf_NESTED001",
				Status:       models.TaskStatusCompleted,
				TaskMode:     models.TaskModeCode,
				SelfReview:   true,
				PRURL:        "https://github.com/owner/repo/pull/42",
				ParentTaskID: strPtr("bf_GRANDPARENT"),
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			child, ok := Plan(&tt.parent)
			if tt.wantNil {
				if ok || child != nil {
					t.Fatalf("Plan = (%+v, %v), want (nil, false)", child, ok)
				}
				return
			}
			if !ok || child == nil {
				t.Fatalf("Plan = (nil, %v), want non-nil child", ok)
			}
			if child.TaskMode != tt.wantMode {
				t.Errorf("TaskMode = %q, want %q", child.TaskMode, tt.wantMode)
			}
		})
	}
}

func TestPlan_ChildShape(t *testing.T) {
	parent := &models.Task{
		ID:         "bf_PARENT_BUDGET",
		Status:     models.TaskStatusCompleted,
		TaskMode:   models.TaskModeCode,
		Harness:    models.HarnessClaudeCode,
		SelfReview: true,
		PRURL:      "https://github.com/owner/repo/pull/42",
		Prompt:     "Refactor the auth flow.",
		// Parent has a high budget; child should not inherit it.
		MaxBudgetUSD: 50.0,
		Model:        "claude-opus-4-7",
		Effort:       "high",
	}

	child, ok := Plan(parent)
	if !ok {
		t.Fatalf("expected chain to produce child, got nil")
	}

	if child.ParentTaskID == nil || *child.ParentTaskID != parent.ID {
		t.Errorf("ParentTaskID = %v, want %q", child.ParentTaskID, parent.ID)
	}
	if child.MaxBudgetUSD != 2.0 {
		t.Errorf("MaxBudgetUSD = %v, want 2.0 (flat budget)", child.MaxBudgetUSD)
	}
	if child.TaskMode != models.TaskModeReview {
		t.Errorf("TaskMode = %q, want review", child.TaskMode)
	}
	if child.Harness != parent.Harness {
		t.Errorf("Harness = %q, want %q (inherited)", child.Harness, parent.Harness)
	}
	if child.Status != models.TaskStatusPending {
		t.Errorf("Status = %q, want pending", child.Status)
	}
	if !strings.Contains(child.Prompt, parent.PRURL) {
		t.Errorf("child Prompt = %q, must reference parent PR URL %q", child.Prompt, parent.PRURL)
	}
	if !strings.Contains(child.Prompt, parent.Prompt) {
		t.Errorf("child Prompt = %q, must reference parent prompt %q for context", child.Prompt, parent.Prompt)
	}
	if !strings.HasPrefix(child.ID, "bf_") {
		t.Errorf("child ID = %q, want bf_ prefix", child.ID)
	}
	if child.ID == parent.ID {
		t.Errorf("child must have a fresh ID, got parent ID %q", child.ID)
	}
	if child.CreatePR {
		t.Errorf("CreatePR = true, want false (review tasks never create PRs)")
	}
	// Cost-related fields on the parent must not leak into the child.
	if child.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0", child.CostUSD)
	}
	if child.PRURL != "" {
		t.Errorf("child PRURL = %q, want empty", child.PRURL)
	}
}

func strPtr(s string) *string { return &s }
