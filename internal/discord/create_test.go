package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/models"
)

// fakeCreateTask returns a CreateTaskFunc that either returns a canned task or an error.
func fakeCreateTask(task *models.Task, err error) CreateTaskFunc {
	return func(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, error) {
		return task, err
	}
}

func fakeTask() *models.Task {
	now := time.Now().UTC()
	return &models.Task{
		ID:        "bf_TEST01",
		Status:    models.TaskStatusPending,
		TaskMode:  models.TaskModeCode,
		Harness:   models.HarnessClaudeCode,
		RepoURL:   "https://github.com/owner/repo",
		Prompt:    "Add tests",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// --- /backflow create command ---

func TestInteractionHandler_CreateCommand_OpensModal(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: fakeCreateTask(fakeTask(), nil)})

	body := `{"type":2,"data":{"name":"backflow","options":[{"name":"create","type":1}]}}`
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ModalResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Type != ResponseTypeModal {
		t.Errorf("response type = %d, want %d (modal)", resp.Type, ResponseTypeModal)
	}
	if resp.Data.Title == "" {
		t.Error("modal title should not be empty")
	}
	if resp.Data.CustomID != modalIDCreate {
		t.Errorf("custom_id = %q, want %q", resp.Data.CustomID, modalIDCreate)
	}
	if len(resp.Data.Components) != 3 {
		t.Errorf("components = %d, want 3", len(resp.Data.Components))
	}
}

// --- Modal submit ---

func buildModalSubmitBody(customID string, fields map[string]string) string {
	rows := make([]map[string]any, 0, len(fields))
	for id, val := range fields {
		rows = append(rows, map[string]any{
			"type": ComponentTypeActionRow,
			"components": []map[string]any{{
				"type":      ComponentTypeTextInput,
				"custom_id": id,
				"value":     val,
			}},
		})
	}
	payload := map[string]any{
		"type": InteractionTypeModalSubmit,
		"data": map[string]any{
			"custom_id":  customID,
			"components": rows,
		},
	}
	b, _ := json.Marshal(payload)
	return string(b)
}

func TestInteractionHandler_ModalSubmit_Success(t *testing.T) {
	pub, priv := testKeyPair(t)
	created := fakeTask()
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: fakeCreateTask(created, nil)})

	body := buildModalSubmitBody(modalIDCreate, map[string]string{
		fieldPrompt: "Add tests",
	})
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Type != ResponseTypeChannelMessage {
		t.Errorf("response type = %d, want %d", resp.Type, ResponseTypeChannelMessage)
	}
	if !strings.Contains(resp.Data.Content, created.ID) {
		t.Errorf("content = %q, want task ID %s", resp.Data.Content, created.ID)
	}
}

func TestInteractionHandler_ModalSubmit_MissingPrompt(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: fakeCreateTask(fakeTask(), nil)})

	body := buildModalSubmitBody(modalIDCreate, map[string]string{})
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Data.Content, "prompt is required") {
		t.Errorf("content = %q, want prompt required message", resp.Data.Content)
	}
}

func TestInteractionHandler_ModalSubmit_InvalidBudget(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: fakeCreateTask(fakeTask(), nil)})

	body := buildModalSubmitBody(modalIDCreate, map[string]string{
		fieldPrompt:    "Add tests",
		fieldBudgetUSD: "not-a-number",
	})
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Data.Content, "Invalid budget") {
		t.Errorf("content = %q, want invalid budget message", resp.Data.Content)
	}
}

func TestInteractionHandler_ModalSubmit_NilCreator(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{})

	body := buildModalSubmitBody(modalIDCreate, map[string]string{
		fieldPrompt: "Add tests",
	})
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Data.Content, "unavailable") {
		t.Errorf("content = %q, want unavailable message", resp.Data.Content)
	}
}

func TestInteractionHandler_ModalSubmit_CreatorError(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: fakeCreateTask(nil, fmt.Errorf("db connection refused"))})

	body := buildModalSubmitBody(modalIDCreate, map[string]string{
		fieldPrompt: "Add tests",
	})
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	var resp ChannelMessageResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp.Data.Content, "Failed to create task") {
		t.Errorf("content = %q, want failure message", resp.Data.Content)
	}
}

func TestInteractionHandler_ModalSubmit_WithHarnessAndBudget(t *testing.T) {
	pub, priv := testKeyPair(t)
	var capturedReq *models.CreateTaskRequest
	creator := CreateTaskFunc(func(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, error) {
		capturedReq = req
		return fakeTask(), nil
	})
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: creator})

	body := buildModalSubmitBody(modalIDCreate, map[string]string{
		fieldPrompt:    "Refactor auth",
		fieldHarness:   "claude_code",
		fieldBudgetUSD: "7.50",
	})
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if capturedReq == nil {
		t.Fatal("createTask was not called")
	}
	if capturedReq.Prompt != "Refactor auth" {
		t.Errorf("Prompt = %q", capturedReq.Prompt)
	}
	if capturedReq.Harness != "claude_code" {
		t.Errorf("Harness = %q, want claude_code", capturedReq.Harness)
	}
	if capturedReq.MaxBudgetUSD != 7.50 {
		t.Errorf("MaxBudgetUSD = %v, want 7.50", capturedReq.MaxBudgetUSD)
	}
}

// --- Modal optional fields ---

func TestCreateModal_OptionalFieldsSerializeRequiredFalse(t *testing.T) {
	pub, priv := testKeyPair(t)
	handler := InteractionHandler(pub, nil, HandlerActions{CreateTask: fakeCreateTask(fakeTask(), nil)})

	body := `{"type":2,"data":{"name":"backflow","options":[{"name":"create","type":1}]}}`
	rr := postInteraction(handler, priv, body)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Parse the raw JSON to check that "required":false is present for optional fields.
	var raw map[string]json.RawMessage
	json.NewDecoder(rr.Body).Decode(&raw)
	rawJSON := string(raw["data"])

	optionalFields := []string{fieldHarness, fieldBudgetUSD}
	for _, field := range optionalFields {
		// Find this field's component and verify required is false.
		var resp ModalResponse
		json.Unmarshal([]byte(`{"type":9,"data":`+rawJSON+`}`), &resp)
		for _, row := range resp.Data.Components {
			for _, comp := range row.Components {
				if comp.CustomID == field && comp.Required {
					t.Errorf("field %q: required = true, want false", field)
				}
			}
		}
	}

	// Also verify the raw JSON contains "required":false (not omitted) for optional fields.
	// This is the actual bug: omitempty drops false bools, and Discord defaults required to true.
	rr = postInteraction(handler, priv, body)
	fullBody := rr.Body.String()
	for _, field := range optionalFields {
		// The field's JSON object should contain "required":false
		idx := strings.Index(fullBody, `"custom_id":"`+field+`"`)
		if idx == -1 {
			t.Fatalf("field %q not found in response JSON", field)
		}
		// Look for the enclosing object (between the previous { and next })
		start := strings.LastIndex(fullBody[:idx], "{")
		end := strings.Index(fullBody[idx:], "}") + idx + 1
		fieldJSON := fullBody[start:end]
		if !strings.Contains(fieldJSON, `"required":false`) {
			t.Errorf("field %q: JSON missing \"required\":false — Discord will default to required.\nGot: %s", field, fieldJSON)
		}
	}
}
