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
		{
			"spot interruption sentinel",
			fmt.Errorf("%w: Host EC2 (spot) terminated", ErrSpotInterruption),
			true,
		},
		{
			"wrapped spot interruption",
			fmt.Errorf("describe ecs task: %w", fmt.Errorf("%w: Fargate Spot capacity reclaimed", ErrSpotInterruption)),
			true,
		},
		{
			"plain string spot not matched",
			fmt.Errorf("spot interruption: Host EC2 (spot) terminated"),
			false,
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
