package discord

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestModalSubmitErrors_AreEphemeral verifies that all error responses from
// modal submit use the ephemeral flag (64) so only the submitting user sees them.
func TestModalSubmitErrors_AreEphemeral(t *testing.T) {
	pub, priv := testKeyPair(t)

	tests := []struct {
		name      string
		createFn  CreateTaskFunc
		fields    map[string]string
		wantInMsg string
	}{
		{
			name:      "missing prompt",
			createFn:  fakeCreateTask(fakeTask(), nil),
			fields:    map[string]string{},
			wantInMsg: "prompt is required",
		},
		{
			name:     "invalid budget",
			createFn: fakeCreateTask(fakeTask(), nil),
			fields: map[string]string{
				fieldPrompt:    "Add tests",
				fieldBudgetUSD: "abc",
			},
			wantInMsg: "Invalid budget",
		},
		{
			name:     "nil creator",
			createFn: nil,
			fields: map[string]string{
				fieldPrompt: "Add tests",
			},
			wantInMsg: "unavailable",
		},
		{
			name:     "creator error",
			createFn: fakeCreateTask(nil, fmt.Errorf("db down")),
			fields: map[string]string{
				fieldPrompt: "Add tests",
			},
			wantInMsg: "Failed to create task",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: tc.createFn})
			customID := modalIDCreate
			body := buildModalSubmitBody(customID, tc.fields)
			rr := postInteraction(handler, priv, body)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}

			var raw map[string]json.RawMessage
			if err := json.NewDecoder(rr.Body).Decode(&raw); err != nil {
				t.Fatalf("decode: %v", err)
			}
			var data struct {
				Flags int `json:"flags"`
			}
			if err := json.Unmarshal(raw["data"], &data); err != nil {
				t.Fatalf("decode data: %v", err)
			}
			if data.Flags != FlagEphemeral {
				t.Errorf("flags = %d, want %d (ephemeral)", data.Flags, FlagEphemeral)
			}
		})
	}
}

// TestModalSubmitSuccess_IsEphemeral verifies that successful task creation
// responses are ephemeral (only visible to the submitting user).
func TestModalSubmitSuccess_IsEphemeral(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: fakeCreateTask(fakeTask(), nil)})

	customID := modalIDCreate
	body := buildModalSubmitBody(customID, map[string]string{
		fieldPrompt: "Add tests",
	})
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rr.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var data struct {
		Flags int `json:"flags"`
	}
	if err := json.Unmarshal(raw["data"], &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data.Flags != FlagEphemeral {
		t.Errorf("flags = %d, want %d (ephemeral)", data.Flags, FlagEphemeral)
	}
}
