// Package imagerouter selects the docker image used to run a given task.
//
// Routing rules (highest priority first):
//
//  1. claude_code + cfg.SkillAgentImage set → skill image. Slice 6 populated
//     the read bundle, so the skill image now hosts every claude_code mode
//     (code, auto, review, read).
//  2. Read mode → cfg.ReaderImage when set. Used for codex read tasks (the
//     skill image is claude_code-only) and for operators who haven't enabled
//     the skill image yet.
//  3. Fall back to cfg.AgentImage.
//
// Codex tasks never go to the skill image (it's claude_code-only).
package imagerouter

import (
	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
)

// Resolve returns the docker image string that should run the given task.
func Resolve(task *models.Task, cfg *config.Config) string {
	if cfg.SkillAgentImage != "" && task.Harness == models.HarnessClaudeCode {
		return cfg.SkillAgentImage
	}
	if task.TaskMode == models.TaskModeRead && cfg.ReaderImage != "" {
		return cfg.ReaderImage
	}
	return cfg.AgentImage
}

// CanRunRead reports whether the configured images can host a read-mode task
// for the given harness. Either ReaderImage (any harness) or SkillAgentImage
// on a claude_code task is enough — the skill image's read bundle was
// populated in slice 6, but the skill image stays claude_code-only, so codex
// read tasks still require ReaderImage.
func CanRunRead(task *models.Task, cfg *config.Config) bool {
	if cfg.ReaderImage != "" {
		return true
	}
	return cfg.SkillAgentImage != "" && task.Harness == models.HarnessClaudeCode
}
