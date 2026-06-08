// Package imagerouter selects the docker image used to run a given task.
//
// Routing rules (highest priority first):
//
//  1. claude_code + cfg.SkillAgentImage set → skill image.
//  2. Fall back to cfg.AgentImage.
//
// Codex tasks never go to the skill image (it's claude_code-only).
package imagerouter

import (
	"fmt"
	"strings"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
)

// Resolve returns the docker image string that should run the given task.
func Resolve(task *models.Task, cfg *config.Config) string {
	if cfg.SkillAgentImage != "" && task.Harness == models.HarnessClaudeCode {
		return cfg.SkillAgentImage
	}
	return cfg.AgentImage
}

// Describe returns a one-line human-readable summary of the routing
// precedence resolved from cfg, suitable for emitting at orchestrator
// startup so operators can confirm which image each task type will land on.
func Describe(cfg *config.Config) string {
	parts := make([]string, 0, 3)
	if cfg.SkillAgentImage != "" {
		parts = append(parts, fmt.Sprintf("claude_code → %s", cfg.SkillAgentImage))
	}
	parts = append(parts, fmt.Sprintf("default → %s", cfg.AgentImage))
	return strings.Join(parts, "; ")
}
