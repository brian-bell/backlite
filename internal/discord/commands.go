package discord

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog/log"
)

const discordAPIBase = "https://discord.com/api/v10"

type slashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        int    `json:"type"`
}

// RegisterCommands registers the Backflow slash commands with Discord using
// the bulk overwrite endpoint. baseURL is overridable for testing; pass "" to
// use the default Discord API.
func RegisterCommands(baseURL, appID, botToken string) error {
	if baseURL == "" {
		baseURL = discordAPIBase
	}

	commands := []slashCommand{
		{
			Name:        "backflow",
			Description: "Check Backflow status",
			Type:        1, // CHAT_INPUT
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
