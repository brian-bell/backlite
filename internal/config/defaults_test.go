package config

import (
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/models"
)

func testConfig() *Config {
	return &Config{
		AgentImage:            "backflow-agent",
		ReaderImage:           "backflow-reader",
		DefaultHarness:        "claude_code",
		DefaultClaudeModel:    "claude-sonnet-4-6",
		DefaultCodexModel:     "gpt-5.4",
		DefaultEffort:         "medium",
		DefaultMaxBudget:      10.0,
		DefaultMaxRuntime:     1800 * time.Second,
		DefaultMaxTurns:       200,
		DefaultReadMaxBudget:  0.5,
		DefaultReadMaxRuntime: 300 * time.Second,
		DefaultReadMaxTurns:   20,
		DefaultCreatePR:       true,
		DefaultSelfReview:     false,
		DefaultSaveOutput:     true,
	}
}

func TestTaskDefaults_CodeMode(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeCode)

	if d.Harness != "claude_code" && d.Harness != "codex" {
		t.Errorf("Harness = %q, want claude_code or codex", d.Harness)
	}
	if d.ClaudeModel == "" {
		t.Error("ClaudeModel is empty")
	}
	if d.CodexModel == "" {
		t.Error("CodexModel is empty")
	}
	if d.Effort != "medium" {
		t.Errorf("Effort = %q, want %q", d.Effort, "medium")
	}
	if d.MaxBudgetUSD != 10.0 {
		t.Errorf("MaxBudgetUSD = %v, want %v", d.MaxBudgetUSD, 10.0)
	}
	if d.MaxRuntimeSec != 1800 {
		t.Errorf("MaxRuntimeSec = %d, want %d", d.MaxRuntimeSec, 1800)
	}
	if d.MaxTurns != 200 {
		t.Errorf("MaxTurns = %d, want %d", d.MaxTurns, 200)
	}
	if !d.CreatePR {
		t.Error("CreatePR = false, want true in code mode")
	}
	if d.SelfReview {
		t.Error("SelfReview = true, want false")
	}
	if !d.SaveAgentOutput {
		t.Error("SaveAgentOutput = false, want true")
	}
}

func TestTaskDefaults_ReviewMode(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeReview)

	if d.CreatePR {
		t.Error("CreatePR = true, want false in review mode")
	}
	// Other defaults unchanged
	if d.Harness != "claude_code" && d.Harness != "codex" {
		t.Errorf("Harness = %q, want claude_code or codex", d.Harness)
	}
	if !d.SaveAgentOutput {
		t.Error("SaveAgentOutput = false, want true")
	}
}

func TestTaskDefaults_ReadMode(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeRead)

	if d.AgentImage != "backflow-reader" {
		t.Errorf("AgentImage = %q, want %q", d.AgentImage, "backflow-reader")
	}
	if d.MaxBudgetUSD != 0.5 {
		t.Errorf("MaxBudgetUSD = %v, want %v (read cap)", d.MaxBudgetUSD, 0.5)
	}
	if d.MaxRuntimeSec != 300 {
		t.Errorf("MaxRuntimeSec = %d, want %d (read cap)", d.MaxRuntimeSec, 300)
	}
	if d.MaxTurns != 20 {
		t.Errorf("MaxTurns = %d, want %d (read cap)", d.MaxTurns, 20)
	}
	if d.CreatePR {
		t.Error("CreatePR = true, want false in read mode")
	}
}

func TestTaskDefaults_CodeMode_AgentImage(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeCode)
	if d.AgentImage != "backflow-agent" {
		t.Errorf("AgentImage = %q, want %q in code mode", d.AgentImage, "backflow-agent")
	}
}

func TestApply_FillsZeroValues(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeCode)
	task := &models.Task{}

	d.Apply(task, nil)

	if task.Harness != models.HarnessClaudeCode && task.Harness != models.HarnessCodex {
		t.Errorf("Harness = %q, want claude_code or codex", task.Harness)
	}
	if task.Model == "" {
		t.Error("Model is empty, want non-empty default")
	}
	if task.Effort != "medium" {
		t.Errorf("Effort = %q, want %q", task.Effort, "medium")
	}
	if task.MaxBudgetUSD != 10.0 {
		t.Errorf("MaxBudgetUSD = %v, want %v", task.MaxBudgetUSD, 10.0)
	}
	if task.MaxRuntimeSec != 1800 {
		t.Errorf("MaxRuntimeSec = %d, want %d", task.MaxRuntimeSec, 1800)
	}
	if task.MaxTurns != 200 {
		t.Errorf("MaxTurns = %d, want %d", task.MaxTurns, 200)
	}
	if !task.CreatePR {
		t.Error("CreatePR = false, want true")
	}
	if task.SelfReview {
		t.Error("SelfReview = true, want false")
	}
	if !task.SaveAgentOutput {
		t.Error("SaveAgentOutput = false, want true")
	}
}

func TestApply_PreservesExplicitValues(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeCode)
	task := &models.Task{
		Harness:       models.HarnessCodex,
		Model:         "custom-model",
		Effort:        "high",
		MaxBudgetUSD:  25.0,
		MaxRuntimeSec: 3600,
		MaxTurns:      500,
	}

	d.Apply(task, nil)

	if task.Harness != models.HarnessCodex {
		t.Errorf("Harness = %q, want %q", task.Harness, models.HarnessCodex)
	}
	if task.Model != "custom-model" {
		t.Errorf("Model = %q, want %q", task.Model, "custom-model")
	}
	if task.Effort != "high" {
		t.Errorf("Effort = %q, want %q", task.Effort, "high")
	}
	if task.MaxBudgetUSD != 25.0 {
		t.Errorf("MaxBudgetUSD = %v, want %v", task.MaxBudgetUSD, 25.0)
	}
	if task.MaxRuntimeSec != 3600 {
		t.Errorf("MaxRuntimeSec = %d, want %d", task.MaxRuntimeSec, 3600)
	}
	if task.MaxTurns != 500 {
		t.Errorf("MaxTurns = %d, want %d", task.MaxTurns, 500)
	}
}

func TestApply_BoolOverrides_Nil(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeCode)
	task := &models.Task{}

	d.Apply(task, nil)

	if !task.CreatePR {
		t.Error("CreatePR = false, want true (default)")
	}
	if task.SelfReview {
		t.Error("SelfReview = true, want false (default)")
	}
	if !task.SaveAgentOutput {
		t.Error("SaveAgentOutput = false, want true (default)")
	}
}

func boolPtr(v bool) *bool { return &v }

func TestApply_BoolOverrides_ExplicitFalse(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeCode)
	task := &models.Task{}

	d.Apply(task, &BoolOverrides{
		CreatePR:        boolPtr(false),
		SaveAgentOutput: boolPtr(false),
	})

	if task.CreatePR {
		t.Error("CreatePR = true, want false (explicit override)")
	}
	if task.SelfReview {
		t.Error("SelfReview = true, want false (default, no override)")
	}
	if task.SaveAgentOutput {
		t.Error("SaveAgentOutput = true, want false (explicit override)")
	}
}

func TestApply_HarnessModelCoupling(t *testing.T) {
	cfg := testConfig()

	// Default harness → should pick matching model
	cfg.DefaultHarness = "claude_code"
	d := cfg.TaskDefaults(models.TaskModeCode)
	task := &models.Task{}
	d.Apply(task, nil)
	claudeModel := task.Model
	if claudeModel == "" {
		t.Fatal("Model is empty for claude_code harness")
	}

	// Switch to codex → model should change
	cfg.DefaultHarness = "codex"
	d = cfg.TaskDefaults(models.TaskModeCode)
	task = &models.Task{}
	d.Apply(task, nil)
	if task.Model == "" {
		t.Fatal("Model is empty for codex harness")
	}
	if task.Model == claudeModel {
		t.Errorf("codex model %q should differ from claude model %q", task.Model, claudeModel)
	}
}

func TestApply_FillsAgentImage(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeRead)
	task := &models.Task{TaskMode: models.TaskModeRead}

	d.Apply(task, nil)

	if task.AgentImage != "backflow-reader" {
		t.Errorf("AgentImage = %q, want %q (from read defaults)", task.AgentImage, "backflow-reader")
	}
}

func TestApply_PreservesExplicitAgentImage(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeCode)
	task := &models.Task{AgentImage: "custom:tag"}

	d.Apply(task, nil)

	if task.AgentImage != "custom:tag" {
		t.Errorf("AgentImage = %q, want %q (preserve explicit)", task.AgentImage, "custom:tag")
	}
}

func TestApply_ReadModeIgnoresCreatePROverride(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeRead)
	task := &models.Task{TaskMode: models.TaskModeRead}

	d.Apply(task, &BoolOverrides{CreatePR: boolPtr(true)})

	if task.CreatePR {
		t.Error("CreatePR = true, want false — read mode should ignore CreatePR override")
	}
}

func TestApply_ReviewModeIgnoresCreatePROverride(t *testing.T) {
	cfg := testConfig()
	d := cfg.TaskDefaults(models.TaskModeReview)
	task := &models.Task{TaskMode: models.TaskModeReview}

	// Caller tries to override CreatePR to true in review mode — should be ignored
	d.Apply(task, &BoolOverrides{
		CreatePR: boolPtr(true),
	})

	if task.CreatePR {
		t.Error("CreatePR = true, want false — review mode should ignore CreatePR override")
	}
}

func TestApply_CallerOverridesHarness(t *testing.T) {
	cfg := testConfig()

	// Get the model each harness resolves to
	cfg.DefaultHarness = "claude_code"
	d := cfg.TaskDefaults(models.TaskModeCode)
	refTask := &models.Task{}
	d.Apply(refTask, nil)
	claudeModel := refTask.Model

	cfg.DefaultHarness = "codex"
	d = cfg.TaskDefaults(models.TaskModeCode)
	refTask = &models.Task{}
	d.Apply(refTask, nil)
	codexModel := refTask.Model

	// Default is codex, but task overrides to claude_code → should get claude model
	d = cfg.TaskDefaults(models.TaskModeCode)
	task := &models.Task{Harness: models.HarnessClaudeCode}
	d.Apply(task, nil)

	if task.Model != claudeModel {
		t.Errorf("Model = %q, want %q when caller overrides harness to claude_code", task.Model, claudeModel)
	}

	// Default is claude_code, but task overrides to codex → should get codex model
	cfg.DefaultHarness = "claude_code"
	d = cfg.TaskDefaults(models.TaskModeCode)
	task = &models.Task{Harness: models.HarnessCodex}
	d.Apply(task, nil)

	if task.Model != codexModel {
		t.Errorf("Model = %q, want %q when caller overrides harness to codex", task.Model, codexModel)
	}
}
