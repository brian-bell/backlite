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
			"wrapped InvalidInstanceId",
			fmt.Errorf("run container: %w", fmt.Errorf("InvalidInstanceId: not found")),
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsInstanceGone(tt.err)
			if got != tt.want {
				t.Errorf("IsInstanceGone(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
