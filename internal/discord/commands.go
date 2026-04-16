package discord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog/log"
)

type slashCommand struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Type        int                  `json:"type"`
	Options     []slashCommandOption `json:"options,omitempty"`
}

type slashCommandOption struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Type        int                  `json:"type"`
	Required    bool                 `json:"required"`
	Options     []slashCommandOption `json:"options,omitempty"`
	Choices     []slashCommandChoice `json:"choices,omitempty"`
}

type slashCommandChoice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RegisterCommands registers the Backflow slash commands with Discord using
// the bulk overwrite endpoint. baseURL is overridable for testing; pass "" to
// use the default Discord API.
func RegisterCommands(baseURL, appID, botToken, commandName string) error {
	if baseURL == "" {
		baseURL = discordAPIBase
	}

	commands := []slashCommand{
		{
			Name:        commandName,
			Description: "Manage Backflow tasks",
			Type:        1, // CHAT_INPUT
			Options: []slashCommandOption{
				{
					Name:        "create",
					Description: "Create a new code task (opens a form)",
					Type:        1, // SUB_COMMAND
				},
				{
					Name:        "status",
					Description: "Look up a task by ID",
					Type:        1, // SUB_COMMAND
					Options: []slashCommandOption{
						{
							Name:        "task_id",
							Description: "Backflow task ID",
							Type:        3, // STRING
							Required:    true,
						},
					},
				},
				{
					Name:        "list",
					Description: "List recent tasks",
					Type:        1, // SUB_COMMAND
					Options: []slashCommandOption{
						{
							Name:        "status",
							Description: "Filter by task status",
							Type:        3, // STRING
							Choices: []slashCommandChoice{
								{Name: "pending", Value: "pending"},
								{Name: "provisioning", Value: "provisioning"},
								{Name: "running", Value: "running"},
								{Name: "completed", Value: "completed"},
								{Name: "failed", Value: "failed"},
								{Name: "interrupted", Value: "interrupted"},
								{Name: "cancelled", Value: "cancelled"},
								{Name: "recovering", Value: "recovering"},
							},
						},
						{
							Name:        "limit",
							Description: "Maximum number of tasks to return",
							Type:        4, // INTEGER
						},
						{
							Name:        "offset",
							Description: "Number of tasks to skip",
							Type:        4, // INTEGER
						},
					},
				},
				{
					Name:        "cancel",
					Description: "Cancel a running task",
					Type:        1, // SUB_COMMAND
					Options: []slashCommandOption{
						{
							Name:        "task_id",
							Description: "Backflow task ID to cancel",
							Type:        3, // STRING
							Required:    true,
						},
					},
				},
				{
					Name:        "retry",
					Description: "Retry a failed or interrupted task",
					Type:        1, // SUB_COMMAND
					Options: []slashCommandOption{
						{
							Name:        "task_id",
							Description: "Backflow task ID to retry",
							Type:        3, // STRING
							Required:    true,
						},
					},
				},
				{
					Name:        "read",
					Description: "Submit a URL for reading",
					Type:        1, // SUB_COMMAND
					Options: []slashCommandOption{
						{
							Name:        "url",
							Description: "The URL to read",
							Type:        3, // STRING
							Required:    true,
						},
						{
							Name:        "force",
							Description: "Bypass duplicate detection and re-read",
							Type:        5, // BOOLEAN
						},
					},
				},
			},
		},
	}

	body, err := json.Marshal(commands)
	if err != nil {
		return fmt.Errorf("marshal commands: %w", err)
	}

	url := fmt.Sprintf("%s/applications/%s/commands", baseURL, appID)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+botToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("discord API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Info().Str("app_id", appID).Msg("discord: slash commands registered")
	return nil
}
