package imagerouter

import (
	"testing"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
)

func TestResolve_TableDriven(t *testing.T) {
	const (
		agentImg = "backlite-agent:v1"
		skillImg = "backlite-skill-agent:v1"
	)

	tests := []struct {
		name     string
		harness  models.Harness
		mode     string
		skillImg string
		want     string
	}{
		{"claude_code+code, skill unset", models.HarnessClaudeCode, models.TaskModeCode, "", agentImg},
		{"claude_code+review, skill unset", models.HarnessClaudeCode, models.TaskModeReview, "", agentImg},
		{"claude_code+auto, skill unset", models.HarnessClaudeCode, models.TaskModeAuto, "", agentImg},
		{"codex+code, skill unset", models.HarnessCodex, models.TaskModeCode, "", agentImg},
		{"codex+review, skill unset", models.HarnessCodex, models.TaskModeReview, "", agentImg},
		{"claude_code+code, skill set", models.HarnessClaudeCode, models.TaskModeCode, skillImg, skillImg},
		{"claude_code+auto, skill set", models.HarnessClaudeCode, models.TaskModeAuto, skillImg, skillImg},
		{"claude_code+review, skill set", models.HarnessClaudeCode, models.TaskModeReview, skillImg, skillImg},
		{"codex+code, skill set", models.HarnessCodex, models.TaskModeCode, skillImg, agentImg},
		{"codex+review, skill set", models.HarnessCodex, models.TaskModeReview, skillImg, agentImg},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AgentImage:      agentImg,
				SkillAgentImage: tt.skillImg,
			}
			task := &models.Task{Harness: tt.harness, TaskMode: tt.mode}
			got := Resolve(task, cfg)
			if got != tt.want {
				t.Errorf("Resolve(%s, %s, skill=%q) = %q, want %q",
					tt.harness, tt.mode, tt.skillImg, got, tt.want)
			}
		})
	}
}

// TestResolve_OverridesPriorAgentImage pins a key behavior: even when the task
// already carries an AgentImage value (set by creation-time defaults), the
// router re-derives at dispatch. This is what makes BACKFLOW_SKILL_AGENT_IMAGE
// a runtime opt-in for in-flight tasks, not just newly created ones.
func TestResolve_OverridesPriorAgentImage(t *testing.T) {
	cfg := &config.Config{
		AgentImage:      "default-agent",
		SkillAgentImage: "skill-agent",
	}
	task := &models.Task{
		Harness:    models.HarnessClaudeCode,
		TaskMode:   models.TaskModeCode,
		AgentImage: "default-agent", // set by creation defaults
	}
	if got := Resolve(task, cfg); got != "skill-agent" {
		t.Errorf("Resolve = %q, want %q (router takes precedence over creation default)", got, "skill-agent")
	}
}

// TestDescribe_SummarizesRouting pins the human-readable startup-log summary
// for each combination of configured images. Operators read this in the
// orchestrator's startup log to confirm which image their tasks will land on.
func TestDescribe_SummarizesRouting(t *testing.T) {
	const (
		agentImg = "agent:v1"
		skillImg = "skill:v1"
	)
	tests := []struct {
		name     string
		agentImg string
		skillImg string
		want     string
	}{
		{
			name:     "agent only",
			agentImg: agentImg,
			want:     "default → agent:v1",
		},
		{
			name:     "agent + skill",
			agentImg: agentImg,
			skillImg: skillImg,
			want:     "claude_code → skill:v1; default → agent:v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AgentImage:      tt.agentImg,
				SkillAgentImage: tt.skillImg,
			}
			if got := Describe(cfg); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}
