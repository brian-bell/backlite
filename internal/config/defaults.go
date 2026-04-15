package config

import "github.com/backflow-labs/backflow/internal/models"

// TaskDefaults holds resolved default values for new tasks.
type TaskDefaults struct {
	AgentImage      string
	Harness         string
	ClaudeModel     string
	CodexModel      string
	Effort          string
	MaxBudgetUSD    float64
	MaxRuntimeSec   int
	MaxTurns        int
	CreatePR        bool
	SelfReview      bool
	SaveAgentOutput bool
}

// BoolOverrides carries optional boolean overrides from the caller.
// A nil pointer means "use the default"; a non-nil pointer means "use this value."
type BoolOverrides struct {
	CreatePR        *bool
	SelfReview      *bool
	SaveAgentOutput *bool
}

// TaskDefaults returns resolved defaults for the given task mode.
func (c *Config) TaskDefaults(taskMode string) TaskDefaults {
	d := TaskDefaults{
		AgentImage:      c.AgentImage,
		Harness:         c.DefaultHarness,
		ClaudeModel:     c.DefaultClaudeModel,
		CodexModel:      c.DefaultCodexModel,
		Effort:          c.DefaultEffort,
		MaxBudgetUSD:    c.DefaultMaxBudget,
		MaxRuntimeSec:   int(c.DefaultMaxRuntime.Seconds()),
		MaxTurns:        c.DefaultMaxTurns,
		CreatePR:        c.DefaultCreatePR,
		SelfReview:      c.DefaultSelfReview,
		SaveAgentOutput: c.DefaultSaveOutput,
	}

	switch taskMode {
	case models.TaskModeReview:
		d.CreatePR = false
	case models.TaskModeRead:
		d.AgentImage = c.ReaderImage
		d.MaxBudgetUSD = c.DefaultReadMaxBudget
		d.MaxRuntimeSec = int(c.DefaultReadMaxRuntime.Seconds())
		d.MaxTurns = c.DefaultReadMaxTurns
		d.CreatePR = false
	}
	// Auto mode uses the same defaults as code mode (superset).

	return d
}

// Apply fills zero-value fields on task with defaults. Boolean fields use
// overrides when non-nil, otherwise the default. Model is resolved based on
// the task's actual harness after defaulting.
func (d TaskDefaults) Apply(task *models.Task, overrides *BoolOverrides) {
	if task.Harness == "" {
		task.Harness = models.Harness(d.Harness)
	}
	if task.Model == "" {
		if task.Harness == models.HarnessCodex {
			task.Model = d.CodexModel
		} else {
			task.Model = d.ClaudeModel
		}
	}
	if task.Effort == "" {
		task.Effort = d.Effort
	}
	if task.AgentImage == "" {
		task.AgentImage = d.AgentImage
	}
	if task.MaxBudgetUSD == 0 {
		task.MaxBudgetUSD = d.MaxBudgetUSD
	}
	if task.MaxRuntimeSec == 0 {
		task.MaxRuntimeSec = d.MaxRuntimeSec
	}
	if task.MaxTurns == 0 {
		task.MaxTurns = d.MaxTurns
	}

	// Booleans: use override if provided, otherwise default.
	// Review and read modes always force CreatePR=false regardless of override.
	if task.TaskMode == models.TaskModeReview || task.TaskMode == models.TaskModeRead {
		task.CreatePR = false
	} else {
		task.CreatePR = boolOrDefault(overrides.createPR(), d.CreatePR)
	}
	task.SelfReview = boolOrDefault(overrides.selfReview(), d.SelfReview)
	task.SaveAgentOutput = boolOrDefault(overrides.saveAgentOutput(), d.SaveAgentOutput)
}

func boolOrDefault(override *bool, def bool) bool {
	if override != nil {
		return *override
	}
	return def
}

func (o *BoolOverrides) createPR() *bool {
	if o == nil {
		return nil
	}
	return o.CreatePR
}

func (o *BoolOverrides) selfReview() *bool {
	if o == nil {
		return nil
	}
	return o.SelfReview
}

func (o *BoolOverrides) saveAgentOutput() *bool {
	if o == nil {
		return nil
	}
	return o.SaveAgentOutput
}
