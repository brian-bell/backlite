// Package imagerouter selects the docker image used to run a given task.
//
// Routing rules (highest priority first):
//
//  1. Read mode → cfg.ReaderImage when set. The skill image's read bundle
//     is a stub today (slice 6), so we never route around the working
//     reader image.
//  2. claude_code + (code | auto) + cfg.SkillAgentImage set → skill image.
//     Other modes' bundles are still stubs (review = slice 5, read =
//     slice 6), so they keep their existing routing to avoid regressing
//     working paths. Once those bundles are real, broaden the gate.
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
	if task.TaskMode == models.TaskModeRead && cfg.ReaderImage != "" {
		return cfg.ReaderImage
	}
	if cfg.SkillAgentImage != "" && task.Harness == models.HarnessClaudeCode {
		switch task.TaskMode {
		case models.TaskModeCode, models.TaskModeAuto:
			return cfg.SkillAgentImage
		}
	}
	return cfg.AgentImage
}

// CanRunRead reports whether the configured images can host a read-mode task.
// Today only ReaderImage handles read — the skill image's read bundle is a
// stub (slice 6), so SkillAgentImage doesn't unlock read on its own. The
// task and harness arguments are kept for the future broadening that lands
// when the read skill becomes real.
func CanRunRead(_ *models.Task, cfg *config.Config) bool {
	return cfg.ReaderImage != ""
}
