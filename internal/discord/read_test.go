package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/backflow-labs/backflow/internal/models"
)

// buildReadCommandBody constructs the JSON for a `/<commandName> read` application-command
// interaction. If forceOpt is non-nil, a `force` option is included. The resulting JSON
// can be wrapped with memberBody to add role info.
func buildReadCommandBody(url string, forceOpt *bool) string {
	subOpts := []map[string]any{
		{"name": "url", "type": 3, "value": url},
	}
	if forceOpt != nil {
		subOpts = append(subOpts, map[string]any{"name": "force", "type": 5, "value": *forceOpt})
	}
	payload := map[string]any{
		"type": InteractionTypeApplicationCommand,
		"data": map[string]any{
			"name": "backflow",
			"options": []map[string]any{
				{"name": "read", "type": 1, "options": subOpts},
			},
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

// recordingCreateTask returns a CreateTaskFunc that records each call into *calls.
func recordingCreateTask(calls *[]*models.CreateTaskRequest, task *models.Task, err error) CreateTaskFunc {
	return func(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, error) {
		*calls = append(*calls, req)
		return task, err
	}
}

func TestValidateReadURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid https URL", input: "https://example.com", want: "https://example.com"},
		{name: "valid URL with path, query, fragment", input: "https://example.com/a/b?x=1#frag", want: "https://example.com/a/b?x=1#frag"},
		{name: "http scheme rejected", input: "http://example.com", wantErr: true},
		{name: "ftp scheme rejected", input: "ftp://example.com", wantErr: true},
		{name: "file scheme rejected", input: "file:///etc/passwd", wantErr: true},
		{name: "trims whitespace", input: "   https://example.com  ", want: "https://example.com"},
		{name: "empty string", input: "", wantErr: true},
		{name: "whitespace only", input: "   ", wantErr: true},
		{name: "bare text no scheme", input: "example.com", wantErr: true},
		{name: "scheme without host", input: "https://", wantErr: true},
		{name: "null byte rejected", input: "https://example.com\x00/abc", wantErr: true},
		{name: "exceeds max length", input: "https://example.com/" + strings.Repeat("a", maxReadURLLength), wantErr: true},
		{name: "at max length is valid", input: "https://example.com/" + strings.Repeat("a", maxReadURLLength-len("https://example.com/")), want: "https://example.com/" + strings.Repeat("a", maxReadURLLength-len("https://example.com/"))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateReadURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateReadURL(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateReadURL(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateReadURL(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestInteractionHandler_ReadCommand_Unauthorized(t *testing.T) {
	pub, priv := testKeyPair(t)
	var calls []*models.CreateTaskRequest
	handler := InteractionHandler(pub, nil, HandlerActions{
		CreateTask:   recordingCreateTask(&calls, fakeTask(), nil),
		AllowedRoles: []string{"admin-role"},
	})

	body := memberBody(buildReadCommandBody("https://example.com", nil), "viewer-role")
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(strings.ToLower(resp.Data.Content), "permission") {
		t.Errorf("content = %q, want permission denied message", resp.Data.Content)
	}
	if resp.Data.Flags != FlagEphemeral {
		t.Errorf("flags = %d, want %d (ephemeral)", resp.Data.Flags, FlagEphemeral)
	}
	if len(calls) != 0 {
		t.Errorf("CreateTask should not have been called, got %d calls", len(calls))
	}
}

func TestInteractionHandler_ReadCommand_InvalidURL(t *testing.T) {
	pub, priv := testKeyPair(t)
	var calls []*models.CreateTaskRequest
	handler := InteractionHandler(pub, nil, HandlerActions{
		CreateTask: recordingCreateTask(&calls, fakeTask(), nil),
	})

	// No scheme → invalid
	body := buildReadCommandBody("not-a-url", nil)
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Flags != FlagEphemeral {
		t.Errorf("flags = %d, want %d (ephemeral)", resp.Data.Flags, FlagEphemeral)
	}
	if !strings.HasPrefix(resp.Data.Content, "Invalid url:") {
		t.Errorf("content = %q, want prefix %q", resp.Data.Content, "Invalid url:")
	}
	if len(calls) != 0 {
		t.Errorf("CreateTask should not have been called, got %d calls", len(calls))
	}
}

func TestInteractionHandler_ReadCommand_HappyPath(t *testing.T) {
	pub, priv := testKeyPair(t)
	var calls []*models.CreateTaskRequest
	handler := InteractionHandler(pub, nil, HandlerActions{
		CreateTask: recordingCreateTask(&calls, fakeTask(), nil),
	})

	url := "https://example.com/article?x=1"
	body := buildReadCommandBody(url, nil)
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Flags != FlagEphemeral {
		t.Errorf("flags = %d, want %d (ephemeral)", resp.Data.Flags, FlagEphemeral)
	}
	wantContent := "Reading " + url + "..."
	if resp.Data.Content != wantContent {
		t.Errorf("content = %q, want %q", resp.Data.Content, wantContent)
	}
	if len(calls) != 1 {
		t.Fatalf("CreateTask calls = %d, want 1", len(calls))
	}
	req := calls[0]
	if req.Prompt != url {
		t.Errorf("prompt = %q, want %q", req.Prompt, url)
	}
	if req.TaskMode == nil || *req.TaskMode != models.TaskModeRead {
		modeStr := "<nil>"
		if req.TaskMode != nil {
			modeStr = *req.TaskMode
		}
		t.Errorf("task_mode = %q, want %q", modeStr, models.TaskModeRead)
	}
	if req.Force != nil && *req.Force {
		t.Errorf("force = %v, want nil or false", *req.Force)
	}
}

func TestInteractionHandler_ReadCommand_Force(t *testing.T) {
	pub, priv := testKeyPair(t)
	var calls []*models.CreateTaskRequest
	handler := InteractionHandler(pub, nil, HandlerActions{
		CreateTask: recordingCreateTask(&calls, fakeTask(), nil),
	})

	force := true
	body := buildReadCommandBody("https://example.com", &force)
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if len(calls) != 1 {
		t.Fatalf("CreateTask calls = %d, want 1", len(calls))
	}
	req := calls[0]
	if req.Force == nil {
		t.Fatalf("force = nil, want non-nil true")
	}
	if !*req.Force {
		t.Errorf("force = false, want true")
	}
}

func TestInteractionHandler_ReadCommand_NoCreateTask(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{})

	body := buildReadCommandBody("https://example.com", nil)
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Flags != FlagEphemeral {
		t.Errorf("flags = %d, want %d (ephemeral)", resp.Data.Flags, FlagEphemeral)
	}
	if !strings.Contains(strings.ToLower(resp.Data.Content), "unavailable") {
		t.Errorf("content = %q, want 'unavailable' message", resp.Data.Content)
	}
}

func TestInteractionHandler_ReadCommand_CreateTaskError(t *testing.T) {
	pub, priv := testKeyPair(t)
	var calls []*models.CreateTaskRequest
	handler := InteractionHandler(pub, nil, HandlerActions{
		CreateTask: recordingCreateTask(&calls, nil, fmt.Errorf("db connection refused")),
	})

	body := buildReadCommandBody("https://example.com", nil)
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Flags != FlagEphemeral {
		t.Errorf("flags = %d, want %d (ephemeral)", resp.Data.Flags, FlagEphemeral)
	}
	if !strings.Contains(resp.Data.Content, "db connection refused") {
		t.Errorf("content = %q, want error text to be surfaced", resp.Data.Content)
	}
}
