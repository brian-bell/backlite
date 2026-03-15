package orchestrator

import (
	"fmt"
	"testing"
)

func TestIsInstanceGone(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"unrelated error", fmt.Errorf("connection timeout"), false},
		{
			"InvalidInstanceId from SSM",
			fmt.Errorf("ssm send command: operation error SSM: SendCommand, https response error StatusCode: 400, InvalidInstanceId: Instances not in a valid state for account"),
			true,
		},
		{
			"InvalidInstanceID variant",
			fmt.Errorf("InvalidInstanceID: i-1234567890abcdef0"),
			true,
		},
		{
			"wrapped error",
			fmt.Errorf("run container: %w", fmt.Errorf("ssm send command: InvalidInstanceId: not found")),
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isInstanceGone(tt.err)
			if got != tt.want {
				t.Errorf("isInstanceGone(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"", "''"},
		{"it's a test", "'it'\"'\"'s a test'"},
		{"no special chars", "'no special chars'"},
		{"multi'quote'test", "'multi'\"'\"'quote'\"'\"'test'"},
		{"spaces and\ttabs", "'spaces and\ttabs'"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shellEscape(tt.input)
			if got != tt.want {
				t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsHexString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid lowercase", "abcdef0123456789", true},
		{"valid uppercase", "ABCDEF0123456789", true},
		{"valid mixed", "aAbBcC123", true},
		{"valid short", "a", true},
		{"empty string", "", false},
		{"contains g", "abcdefg", false},
		{"contains space", "abc def", false},
		{"contains dash", "abc-def", false},
		{"typical container id", "d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHexString(tt.input)
			if got != tt.want {
				t.Errorf("isHexString(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
