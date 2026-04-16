package discord

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
)

// maxReadURLLength caps the length of a URL submitted to /backflow read.
// Keeps pathological inputs out of the task prompt and DB while leaving
// comfortable headroom over the common 2048-byte browser limit.
const maxReadURLLength = 8192

// ValidateReadURL validates that input is a well-formed https:// URL with a
// host and no control characters, and that it does not exceed maxReadURLLength.
// It does not check reachability or restrict domains. The returned string is
// the trimmed input on success.
func ValidateReadURL(input string) (string, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", fmt.Errorf("url is required")
	}
	if len(trimmed) > maxReadURLLength {
		return "", fmt.Errorf("url exceeds %d characters", maxReadURLLength)
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("url must use https scheme")
	}
	if u.Host == "" {
		return "", fmt.Errorf("url must include a host")
	}
	return trimmed, nil
}

// handleReadCommand processes a `/backflow read` application-command interaction.
func handleReadCommand(ctx context.Context, w http.ResponseWriter, interaction Interaction, options []CommandOption, actions HandlerActions) {
	if !hasPermission(interaction.Member, actions.AllowedRoles) {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: "You don't have permission to read URLs.", Flags: FlagEphemeral},
		})
		return
	}
	rawURL, err := stringOption(options, "url")
	if err != nil {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: err.Error(), Flags: FlagEphemeral},
		})
		return
	}
	validated, err := ValidateReadURL(rawURL)
	if err != nil {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Invalid url: %s", err.Error()), Flags: FlagEphemeral},
		})
		return
	}
	readMode := models.TaskModeRead
	req := &models.CreateTaskRequest{
		Prompt:   validated,
		TaskMode: &readMode,
	}
	if force, ok := boolOption(options, "force"); ok {
		req.Force = &force
	}
	if actions.CreateTask == nil {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: "Task creation is unavailable right now.", Flags: FlagEphemeral},
		})
		return
	}
	task, err := actions.CreateTask(ctx, req)
	if err != nil {
		userID := ""
		if interaction.Member != nil && interaction.Member.User != nil {
			userID = interaction.Member.User.ID
		}
		taskID := ""
		if task != nil {
			taskID = task.ID
		}
		log.Warn().
			Err(err).
			Str("url", validated).
			Str("guild_id", interaction.GuildID).
			Str("user_id", userID).
			Str("task_id", taskID).
			Msg("discord: failed to create read task")
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Failed to create reading task: %s", err.Error()), Flags: FlagEphemeral},
		})
		return
	}
	respondJSON(w, ChannelMessageResponse{
		Type: ResponseTypeChannelMessage,
		Data: MessageData{Content: fmt.Sprintf("Reading %s...", validated), Flags: FlagEphemeral},
	})
}
