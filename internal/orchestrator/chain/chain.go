// Package chain encapsulates the chained-task primitive used by self-review.
//
// A code task with self_review=true that completes successfully and produced
// a PR URL gets a follow-up review task: same repo context, prompt synthesized
// from the parent (referencing the PR URL), parent_task_id pointing at the
// parent, and a flat $2 budget.
//
// Plan is a pure function — given a parent task it returns the child to insert
// (or nothing) — so the chain rule is testable without spinning up a store.
// Atomicity (parent COMPLETE + child INSERT in a single SQLite tx) is achieved
// by the lifecycle.Coordinator's ChainTx hook, which runs Plan and the child
// insert inside the same WithTx that writes the parent's terminal state.
package chain

import (
	"context"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/store"
)

// Flat budget applied to chained self-review tasks regardless of the parent's
// budget. The PRD pins this at $2 to make self-review cost predictable.
const SelfReviewBudgetUSD = 2.00

// Plan returns the child review task to create for a chained self-review,
// or (nil, false) if the parent does not qualify. Pure function — no DB
// access. Eligibility rules:
//
//   - Parent completed successfully
//   - Parent has SelfReview=true
//   - Parent produced a non-empty PR URL
//   - Parent is in code mode. Auto/review/read parents do not chain — by the
//     time a task completes, the agent's prep stage has resolved auto into a
//     concrete mode written to status.json, so an unresolved auto here means
//     the agent never reported back and we shouldn't speculate.
//   - Parent is itself a top-level task (no nested chains)
//
// MaxRuntimeSec, MaxTurns, and AgentImage are intentionally left zero on the
// returned child; the caller fills them from review-mode config defaults
// before persisting (chain stays config-free so it remains pure/testable).
func Plan(parent *models.Task) (*models.Task, bool) {
	if parent.Status != models.TaskStatusCompleted {
		return nil, false
	}
	if !parent.SelfReview {
		return nil, false
	}
	if parent.PRURL == "" {
		return nil, false
	}
	if parent.TaskMode != models.TaskModeCode {
		return nil, false
	}
	if parent.ParentTaskID != nil {
		return nil, false
	}

	parentID := parent.ID
	now := time.Now().UTC()
	child := &models.Task{
		ID:           "bf_" + ulid.Make().String(),
		Status:       models.TaskStatusPending,
		TaskMode:     models.TaskModeReview,
		Harness:      parent.Harness,
		ParentTaskID: &parentID,
		// Inherit RepoURL/Branch so dispatch has them up front. The synthesized
		// prompt also names the PR URL, but relying solely on URL parsing for
		// repo identity is brittle — explicit inheritance is cheap insurance.
		RepoURL:      parent.RepoURL,
		Branch:       parent.Branch,
		Prompt:       synthReviewPrompt(parent),
		Context:      parent.Context,
		Model:        parent.Model,
		Effort:       parent.Effort,
		MaxBudgetUSD: SelfReviewBudgetUSD,
		// Review tasks never open PRs.
		CreatePR:        false,
		SaveAgentOutput: parent.SaveAgentOutput,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	return child, true
}

// synthReviewPrompt builds the chained-review prompt from the parent task. The
// shape is intentionally simple: the agent is told what PR to review and what
// the original code task was trying to accomplish, so it has enough context
// to give meaningful feedback without re-deriving anything from scratch.
func synthReviewPrompt(parent *models.Task) string {
	return fmt.Sprintf(
		"Review the changes in %s. The original task was: %s",
		parent.PRURL, parent.Prompt,
	)
}

// CreateChild inserts the child task using the supplied store. Callers that
// want atomicity with parent completion pass the tx-scoped store from a
// lifecycle ChainTx callback; callers that just need fire-and-forget can pass
// the top-level store.
func CreateChild(ctx context.Context, s store.Store, child *models.Task) error {
	if child == nil {
		return nil
	}
	return s.CreateTask(ctx, child)
}
