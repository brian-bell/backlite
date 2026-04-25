// Package imagerouter selects the docker image used to run a given task.
//
// Routing rules (highest priority first):
//
//  1. Skill-agent opt-in: if cfg.SkillAgentImage is set and the task's
//     harness is claude_code, use the skill-agent image regardless of mode.
//     Codex tasks are deliberately excluded (the new image is claude_code-only).
//  2. Read-mode default: if task is read mode and cfg.ReaderImage is set,
//     use the reader image.
//  3. Fall back to cfg.AgentImage.
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
