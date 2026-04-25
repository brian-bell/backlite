// Package skillcontract validates the status.json payload that a skill-based
// agent writes at the end of a run. The contract is shared between the
// orchestrator (which parses status.json into AgentStatus) and the SKILL.md
// instructions that tell the agent what to write — keeping them in sync is
// the whole point of running validation in `make test`.
//
// The schema lives in schema.json (also embedded into the binary) for use by
// external tools and human readers. Validate enforces the parts the
// orchestrator actually depends on: required fields, well-known field types,
// enum constraints on task_mode and novelty_verdict.
package skillcontract

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed schema.json
var SchemaJSON []byte

// requiredFields lists fields the orchestrator can't function without.
var requiredFields = []string{"complete", "needs_input", "task_mode"}

var validTaskModes = map[string]bool{
	"code":   true,
	"review": true,
	"read":   true,
	"auto":   true,
}

var validNoveltyVerdicts = map[string]bool{
	"":                 true,
	"novel":            true,
	"extends_existing": true,
	"duplicate":        true,
}

// Validate parses raw as JSON and confirms it matches the skill-agent status
// contract. Returns a non-nil error describing the first violation found.
func Validate(raw []byte) error {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	for _, key := range requiredFields {
		if _, ok := m[key]; !ok {
			return fmt.Errorf("required field %q is missing", key)
		}
	}

	if err := expectBool(m, "complete"); err != nil {
		return err
	}
	if err := expectBool(m, "needs_input"); err != nil {
		return err
	}

	if err := expectString(m, "task_mode"); err != nil {
		return err
	}
	if mode, _ := m["task_mode"].(string); !validTaskModes[mode] {
		return fmt.Errorf("task_mode %q not in allowed enum {code,review,read,auto}", mode)
	}

	if err := expectOptionalString(m, "question"); err != nil {
		return err
	}
	if err := expectOptionalString(m, "error"); err != nil {
		return err
	}
	if err := expectOptionalString(m, "pr_url"); err != nil {
		return err
	}
	if err := expectOptionalString(m, "repo_url"); err != nil {
		return err
	}
	if err := expectOptionalString(m, "target_branch"); err != nil {
		return err
	}
	if err := expectOptionalNonNegativeNumber(m, "cost_usd"); err != nil {
		return err
	}
	if err := expectOptionalNonNegativeNumber(m, "elapsed_time_sec"); err != nil {
		return err
	}
	if err := expectOptionalNonNegativeNumber(m, "exit_code"); err != nil {
		return err
	}

	// Read-mode fields: optional but when present, must have the right shape.
	if err := expectOptionalString(m, "url"); err != nil {
		return err
	}
	if err := expectOptionalString(m, "title"); err != nil {
		return err
	}
	if err := expectOptionalString(m, "tldr"); err != nil {
		return err
	}
	if err := expectOptionalString(m, "summary_markdown"); err != nil {
		return err
	}
	if err := expectOptionalStringArray(m, "tags"); err != nil {
		return err
	}
	if err := expectOptionalStringArray(m, "keywords"); err != nil {
		return err
	}
	if err := expectOptionalStringArray(m, "people"); err != nil {
		return err
	}
	if err := expectOptionalStringArray(m, "orgs"); err != nil {
		return err
	}
	if v, ok := m["novelty_verdict"]; ok {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("field %q must be a string, got %T", "novelty_verdict", v)
		}
		if !validNoveltyVerdicts[s] {
			return fmt.Errorf("novelty_verdict %q not in allowed enum {novel,extends_existing,duplicate}", s)
		}
	}
	if err := expectOptionalConnections(m, "connections"); err != nil {
		return err
	}

	return nil
}

func expectBool(m map[string]any, key string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	if _, ok := v.(bool); !ok {
		return fmt.Errorf("field %q must be a boolean, got %T", key, v)
	}
	return nil
}

func expectString(m map[string]any, key string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	if _, ok := v.(string); !ok {
		return fmt.Errorf("field %q must be a string, got %T", key, v)
	}
	return nil
}

func expectOptionalString(m map[string]any, key string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	if _, ok := v.(string); !ok {
		return fmt.Errorf("field %q must be a string, got %T", key, v)
	}
	return nil
}

func expectOptionalNonNegativeNumber(m map[string]any, key string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	n, ok := v.(float64)
	if !ok {
		return fmt.Errorf("field %q must be a number, got %T", key, v)
	}
	if n < 0 {
		return fmt.Errorf("field %q must be >= 0, got %v", key, n)
	}
	return nil
}

func expectOptionalStringArray(m map[string]any, key string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("field %q must be an array, got %T", key, v)
	}
	for i, el := range arr {
		if _, ok := el.(string); !ok {
			return fmt.Errorf("field %q[%d] must be a string, got %T", key, i, el)
		}
	}
	return nil
}

func expectOptionalConnections(m map[string]any, key string) error {
	v, ok := m[key]
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return fmt.Errorf("field %q must be an array, got %T", key, v)
	}
	for i, el := range arr {
		obj, ok := el.(map[string]any)
		if !ok {
			return fmt.Errorf("field %q[%d] must be an object, got %T", key, i, el)
		}
		if _, ok := obj["reading_id"].(string); !ok {
			return fmt.Errorf("field %q[%d].reading_id must be a non-empty string", key, i)
		}
		if _, ok := obj["reason"].(string); !ok {
			return fmt.Errorf("field %q[%d].reason must be a non-empty string", key, i)
		}
	}
	return nil
}
