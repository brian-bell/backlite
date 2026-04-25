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

// CanRunRead reports whether the configured images can host a read-mode task.
// True when ReaderImage is set, or when SkillAgentImage is set for a
// claude_code task (the skill bundle ships the read skill). codex tasks
// require ReaderImage because the skill image is claude_code-only.
//
// Dispatch should consult this before starting a read-mode container so
// operators who run all modes through SkillAgentImage don't need to also
// configure BACKFLOW_READER_IMAGE.
func CanRunRead(task *models.Task, cfg *config.Config) bool {
	if cfg.SkillAgentImage != "" && task.Harness == models.HarnessClaudeCode {
		return true
	}
	return cfg.ReaderImage != ""
}
