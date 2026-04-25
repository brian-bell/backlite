package skillcontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate_HappyPath_CodeMode(t *testing.T) {
	good := []byte(`{
		"exit_code": 0,
		"complete": true,
		"needs_input": false,
		"pr_url": "https://github.com/owner/repo/pull/42",
		"cost_usd": 0.42,
		"elapsed_time_sec": 120,
		"repo_url": "https://github.com/owner/repo",
		"target_branch": "main",
		"task_mode": "code"
	}`)
	if err := Validate(good); err != nil {
		t.Errorf("Validate(good) = %v, want nil", err)
	}
}

func TestValidate_HappyPath_ReadMode(t *testing.T) {
	good := []byte(`{
		"complete": true,
		"needs_input": false,
		"task_mode": "read",
		"url": "https://example.com/post",
		"title": "Some Post",
		"tldr": "A short summary.",
		"tags": ["a", "b"],
		"keywords": ["x"],
		"people": [],
		"orgs": [],
		"novelty_verdict": "novel",
		"connections": [],
		"summary_markdown": "## Summary"
	}`)
	if err := Validate(good); err != nil {
		t.Errorf("Validate(good read) = %v, want nil", err)
	}
}

func TestValidate_RejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string // expected substring of error
	}{
		{"missing complete", `{"needs_input": false, "task_mode": "code"}`, "complete"},
		{"missing needs_input", `{"complete": true, "task_mode": "code"}`, "needs_input"},
		{"missing task_mode", `{"complete": true, "needs_input": false}`, "task_mode"},
		{"empty body", `{}`, "complete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate([]byte(tt.body))
			if err == nil {
				t.Fatalf("Validate(%q) = nil, want error", tt.body)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestValidate_RejectsWrongTypes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"complete is string", `{"complete": "yes", "needs_input": false, "task_mode": "code"}`},
		{"needs_input is int", `{"complete": true, "needs_input": 1, "task_mode": "code"}`},
		{"task_mode is array", `{"complete": true, "needs_input": false, "task_mode": []}`},
		{"cost_usd is string", `{"complete": true, "needs_input": false, "task_mode": "code", "cost_usd": "0.5"}`},
		{"cost_usd negative", `{"complete": true, "needs_input": false, "task_mode": "code", "cost_usd": -1}`},
		{"task_mode invalid enum", `{"complete": true, "needs_input": false, "task_mode": "garbage"}`},
		{"tags element non-string", `{"complete": true, "needs_input": false, "task_mode": "read", "tags": [1,2]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate([]byte(tt.body)); err == nil {
				t.Errorf("Validate(%q) = nil, want error", tt.body)
			}
		})
	}
}

func TestValidate_AllowsExtraFields(t *testing.T) {
	body := []byte(`{
		"complete": true,
		"needs_input": false,
		"task_mode": "code",
		"agent_internal_field": "for future use"
	}`)
	if err := Validate(body); err != nil {
		t.Errorf("Validate with extra fields = %v, want nil (extra fields are tolerated)", err)
	}
}

func TestValidate_RejectsInvalidJSON(t *testing.T) {
	if err := Validate([]byte("not json")); err == nil {
		t.Fatal("Validate(garbage) = nil, want error")
	}
}

// TestSkillExamples_ValidateAgainstSchema walks the docker/skill-agent/skills
// tree and validates every examples/status.json fixture. Drift between the
// SKILL.md instructions and the orchestrator-side parser would show up here.
func TestSkillExamples_ValidateAgainstSchema(t *testing.T) {
	skillsDir := filepath.Join("..", "..", "docker", "skill-agent", "skills")

	matches, err := filepath.Glob(filepath.Join(skillsDir, "*", "examples", "status.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Skip("no skill examples to validate yet")
	}

	for _, fp := range matches {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(fp))), func(t *testing.T) {
			body, err := os.ReadFile(fp)
			if err != nil {
				t.Fatalf("read %s: %v", fp, err)
			}
			if err := Validate(body); err != nil {
				t.Errorf("%s: validation failed: %v", fp, err)
			}
		})
	}
}

// TestBrokenFixture_FailsValidation pins the negative case: a deliberately
// malformed fixture must not pass the validator. The fixture lives next to
// the happy-path one and is exempt from TestSkillExamples_ValidateAgainstSchema.
func TestBrokenFixture_FailsValidation(t *testing.T) {
	fp := filepath.Join("..", "..", "docker", "skill-agent", "skills", "code", "examples", "status_broken.json")
	body, err := os.ReadFile(fp)
	if err != nil {
		t.Skipf("broken fixture not found at %s: %v", fp, err)
	}
	if err := Validate(body); err == nil {
		t.Errorf("broken fixture %s passed validation; expected failure", fp)
	}
}
