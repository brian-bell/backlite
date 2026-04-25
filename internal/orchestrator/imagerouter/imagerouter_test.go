package imagerouter

import (
	"testing"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
)

func TestResolve_TableDriven(t *testing.T) {
	const (
		agentImg  = "backlite-agent:v1"
		readerImg = "backlite-reader:v1"
		skillImg  = "backlite-skill-agent:v1"
	)

	tests := []struct {
		name      string
		harness   models.Harness
		mode      string
		skillImg  string
		readerImg string
		want      string
	}{
		// SkillAgentImage unset: behavior matches today's logic.
		{"claude_code+code, skill unset", models.HarnessClaudeCode, models.TaskModeCode, "", readerImg, agentImg},
		{"claude_code+review, skill unset", models.HarnessClaudeCode, models.TaskModeReview, "", readerImg, agentImg},
		{"claude_code+read, skill unset", models.HarnessClaudeCode, models.TaskModeRead, "", readerImg, readerImg},
		{"claude_code+auto, skill unset", models.HarnessClaudeCode, models.TaskModeAuto, "", readerImg, agentImg},
		{"codex+code, skill unset", models.HarnessCodex, models.TaskModeCode, "", readerImg, agentImg},
		{"codex+review, skill unset", models.HarnessCodex, models.TaskModeReview, "", readerImg, agentImg},
		{"codex+read, skill unset", models.HarnessCodex, models.TaskModeRead, "", readerImg, readerImg},

		// SkillAgentImage set + claude_code: route to skill image for every
		// mode (code + auto + review + read, slice 6 populated the read
		// bundle so the skill image is now the unified path for claude_code).
		{"claude_code+code, skill set", models.HarnessClaudeCode, models.TaskModeCode, skillImg, readerImg, skillImg},
		{"claude_code+auto, skill set", models.HarnessClaudeCode, models.TaskModeAuto, skillImg, readerImg, skillImg},
		{"claude_code+review, skill set", models.HarnessClaudeCode, models.TaskModeReview, skillImg, readerImg, skillImg},
		{"claude_code+read, skill set (skill wins over reader)", models.HarnessClaudeCode, models.TaskModeRead, skillImg, readerImg, skillImg},
		{"claude_code+read, skill set + reader unset", models.HarnessClaudeCode, models.TaskModeRead, skillImg, "", skillImg},

		// SkillAgentImage set + codex: codex tasks still use old images.
		{"codex+code, skill set", models.HarnessCodex, models.TaskModeCode, skillImg, readerImg, agentImg},
		{"codex+review, skill set", models.HarnessCodex, models.TaskModeReview, skillImg, readerImg, agentImg},
		{"codex+read, skill set", models.HarnessCodex, models.TaskModeRead, skillImg, readerImg, readerImg},

		// Reader image unset: read mode falls back to agent image (operator
		// hasn't enabled read mode at all).
		{"claude_code+read, reader unset, skill unset", models.HarnessClaudeCode, models.TaskModeRead, "", "", agentImg},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AgentImage:      agentImg,
				ReaderImage:     tt.readerImg,
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

// TestCanRunRead_TableDriven pins that read-mode dispatch is gated by either
// ReaderImage (any harness) or SkillAgentImage on a claude_code task — slice
// 6 populated the read bundle, so the skill image is now a valid host for
// read tasks. Codex tasks still need ReaderImage because the skill image is
// claude_code-only.
func TestCanRunRead_TableDriven(t *testing.T) {
	const (
		readerImg = "backlite-reader:v1"
		skillImg  = "backlite-skill-agent:v1"
	)

	tests := []struct {
		name      string
		harness   models.Harness
		skillImg  string
		readerImg string
		want      bool
	}{
		{"claude_code, both unset", models.HarnessClaudeCode, "", "", false},
		{"claude_code, reader set", models.HarnessClaudeCode, "", readerImg, true},
		{"claude_code, skill set only", models.HarnessClaudeCode, skillImg, "", true},
		{"claude_code, both set", models.HarnessClaudeCode, skillImg, readerImg, true},

		{"codex, both unset", models.HarnessCodex, "", "", false},
		{"codex, reader set", models.HarnessCodex, "", readerImg, true},
		{"codex, skill set only (codex can't use skill image)", models.HarnessCodex, skillImg, "", false},
		{"codex, both set", models.HarnessCodex, skillImg, readerImg, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				ReaderImage:     tt.readerImg,
				SkillAgentImage: tt.skillImg,
			}
			task := &models.Task{Harness: tt.harness, TaskMode: models.TaskModeRead}
			if got := CanRunRead(task, cfg); got != tt.want {
				t.Errorf("CanRunRead(harness=%s, skill=%q, reader=%q) = %v, want %v",
					tt.harness, tt.skillImg, tt.readerImg, got, tt.want)
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
		agentImg  = "agent:v1"
		readerImg = "reader:v1"
		skillImg  = "skill:v1"
	)
	tests := []struct {
		name      string
		agentImg  string
		readerImg string
		skillImg  string
		want      string
	}{
		{
			name:     "agent only",
			agentImg: agentImg,
			want:     "default → agent:v1",
		},
		{
			name:      "agent + reader",
			agentImg:  agentImg,
			readerImg: readerImg,
			want:      "read → reader:v1; default → agent:v1",
		},
		{
			name:     "agent + skill",
			agentImg: agentImg,
			skillImg: skillImg,
			want:     "claude_code → skill:v1; default → agent:v1",
		},
		{
			name:      "agent + reader + skill",
			agentImg:  agentImg,
			readerImg: readerImg,
			skillImg:  skillImg,
			want:      "claude_code → skill:v1; codex+read → reader:v1; default → agent:v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				AgentImage:      tt.agentImg,
				ReaderImage:     tt.readerImg,
				SkillAgentImage: tt.skillImg,
			}
			if got := Describe(cfg); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}
