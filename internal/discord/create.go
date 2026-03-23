package discord

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
)

// Discord component type constants.
const (
	ComponentTypeActionRow = 1
	ComponentTypeButton    = 2
	ComponentTypeTextInput = 4
)

// TextInput styles.
const (
	TextInputStyleShort     = 1
	TextInputStyleParagraph = 2
)

// ResponseTypeModal is the Discord interaction response type for opening a modal.
const ResponseTypeModal = 9

// Modal field custom_id constants.
const (
	modalIDCreate  = "backflow_create"
	fieldPrompt    = "prompt"
	fieldHarness   = "harness"
	fieldBudgetUSD = "budget_usd"
)

// CreateTaskFunc creates a new Backflow task from a request.
// It should validate the request, persist it, and emit a task.created event.
type CreateTaskFunc func(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, error)

// ModalResponse is the Discord response that opens a modal dialog.
type ModalResponse struct {
	Type int       `json:"type"`
	Data ModalData `json:"data"`
}

// ModalData describes the modal to show the user.
type ModalData struct {
	CustomID   string      `json:"custom_id"`
	Title      string      `json:"title"`
	Components []ActionRow `json:"components"`
}

// ActionRow wraps one or more components in a row container.
type ActionRow struct {
	Type       int         `json:"type"`
	Components []TextInput `json:"components"`
}

// TextInput is a single text field inside a modal action row.
// The Value field is populated when parsing modal submit data.
type TextInput struct {
	Type        int    `json:"type"`
	CustomID    string `json:"custom_id"`
	Label       string `json:"label,omitempty"`
	Style       int    `json:"style,omitempty"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
	MaxLength   int    `json:"max_length,omitempty"`
	Value       string `json:"value,omitempty"`
}

// ModalSubmitData is the parsed data from an MODAL_SUBMIT interaction.
type ModalSubmitData struct {
	CustomID   string      `json:"custom_id"`
	Components []ActionRow `json:"components"`
}

// openCreateModal responds with a Discord modal for code task creation.
func openCreateModal(w http.ResponseWriter) {
	modal := ModalResponse{
		Type: ResponseTypeModal,
		Data: ModalData{
			CustomID: modalIDCreate,
			Title:    "Create Backflow Task",
			Components: []ActionRow{
				{
					Type: ComponentTypeActionRow,
					Components: []TextInput{{
						Type:        ComponentTypeTextInput,
						CustomID:    fieldPrompt,
						Label:       "Task description",
						Style:       TextInputStyleParagraph,
						Required:    true,
						Placeholder: "Describe what you want the agent to do (include repo URL)...",
						MaxLength:   2000,
					}},
				},
				{
					Type: ComponentTypeActionRow,
					Components: []TextInput{{
						Type:        ComponentTypeTextInput,
						CustomID:    fieldHarness,
						Label:       "Harness (optional)",
						Style:       TextInputStyleShort,
						Required:    false,
						Placeholder: "claude_code or codex",
					}},
				},
				{
					Type: ComponentTypeActionRow,
					Components: []TextInput{{
						Type:        ComponentTypeTextInput,
						CustomID:    fieldBudgetUSD,
						Label:       "Max budget in USD (optional)",
						Style:       TextInputStyleShort,
						Required:    false,
						Placeholder: "e.g. 5.00",
					}},
				},
			},
		},
	}
	respondJSON(w, modal)
}

// handleCreateSubmit processes a modal submit interaction for task creation.
func handleCreateSubmit(ctx context.Context, w http.ResponseWriter, data ModalSubmitData, createTask CreateTaskFunc) {
	// Extract modal field values.
	fields := extractModalFields(data.Components)
	prompt := strings.TrimSpace(fields[fieldPrompt])
	harness := strings.TrimSpace(fields[fieldHarness])
	budgetStr := strings.TrimSpace(fields[fieldBudgetUSD])

	// Validate required fields locally before calling CreateTaskFunc.
	if prompt == "" {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: "prompt is required.", Flags: FlagEphemeral},
		})
		return
	}

	// Parse optional budget.
	var budgetUSD float64
	if budgetStr != "" {
		var err error
		budgetUSD, err = strconv.ParseFloat(budgetStr, 64)
		if err != nil || budgetUSD < 0 {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: fmt.Sprintf("Invalid budget %q: must be a non-negative number (e.g. 5.00).", budgetStr), Flags: FlagEphemeral},
			})
			return
		}
	}

	req := &models.CreateTaskRequest{
		Prompt:       prompt,
		Harness:      harness,
		MaxBudgetUSD: budgetUSD,
	}

	if createTask == nil {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: "Task creation is unavailable right now.", Flags: FlagEphemeral},
		})
		return
	}

	task, err := createTask(ctx, req)
	if err != nil {
		log.Warn().Err(err).Msg("discord: failed to create task from modal")
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Failed to create task: %s", err.Error()), Flags: FlagEphemeral},
		})
		return
	}

	respondJSON(w, ChannelMessageResponse{
		Type: ResponseTypeChannelMessage,
		Data: MessageData{Content: formatCreatedTask(task), Flags: FlagEphemeral},
	})
}

// extractModalFields walks the action rows returned in a modal submit and
// returns a map of custom_id → value for all TEXT_INPUT components.
func extractModalFields(rows []ActionRow) map[string]string {
	out := make(map[string]string)
	for _, row := range rows {
		if row.Type != ComponentTypeActionRow {
			continue
		}
		for _, comp := range row.Components {
			if comp.Type == ComponentTypeTextInput {
				out[comp.CustomID] = comp.Value
			}
		}
	}
	return out
}

// formatCreatedTask produces a user-facing confirmation for a newly created task.
func formatCreatedTask(task *models.Task) string {
	parts := []string{
		fmt.Sprintf("Task created: **%s**", task.ID),
		fmt.Sprintf("Status: %s", task.Status),
	}
	if task.Harness != "" {
		parts = append(parts, fmt.Sprintf("Harness: %s", task.Harness))
	}
	return strings.Join(parts, "\n")
}
