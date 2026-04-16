package discord

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

// Discord interaction types.
const (
	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2
	InteractionTypeMessageComponent   = 3
	InteractionTypeModalSubmit        = 5
)

// Discord interaction response types.
const (
	ResponseTypePong                   = 1
	ResponseTypeChannelMessage         = 4
	ResponseTypeDeferredChannelMessage = 5
)

// Button custom ID prefixes used by Backflow buttons.
const (
	CustomIDCancelPrefix = "bf_cancel:"
	CustomIDRetryPrefix  = "bf_retry:"
)

// Interaction is the minimal Discord interaction payload needed for routing.
type Interaction struct {
	Type    int             `json:"type"`
	Data    json.RawMessage `json:"data,omitempty"`
	GuildID string          `json:"guild_id,omitempty"`
	Member  *MemberInfo     `json:"member,omitempty"`
}

// MemberInfo holds the guild member information sent with an interaction.
type MemberInfo struct {
	Roles []string     `json:"roles"`
	User  *DiscordUser `json:"user,omitempty"`
}

// DiscordUser carries the subset of the Discord user object used by Backflow.
type DiscordUser struct {
	ID string `json:"id"`
}

// CommandData contains the parsed command name from an application command interaction.
type CommandData struct {
	Name    string          `json:"name"`
	Options []CommandOption `json:"options,omitempty"`
}

// CommandOption captures the subset of Discord option data needed for Backflow.
type CommandOption struct {
	Name    string          `json:"name"`
	Type    int             `json:"type"`
	Value   json.RawMessage `json:"value,omitempty"`
	Options []CommandOption `json:"options,omitempty"`
}

// ComponentData is the data from a message component (button click) interaction.
type ComponentData struct {
	CustomID      string `json:"custom_id"`
	ComponentType int    `json:"component_type"`
}

// InteractionResponse is sent back to Discord.
type InteractionResponse struct {
	Type int `json:"type"`
}

// ChannelMessageResponse sends an immediate message back to the channel.
type ChannelMessageResponse struct {
	Type int         `json:"type"`
	Data MessageData `json:"data"`
}

// FlagEphemeral is the Discord message flag that makes a response visible
// only to the user who triggered the interaction.
const FlagEphemeral = 64

// MessageData is the content payload inside a channel message response.
type MessageData struct {
	Content string `json:"content"`
	Flags   int    `json:"flags,omitempty"`
}

// CancelTaskFunc cancels a task by ID. It is responsible for all validation,
// state changes, and event emission.
type CancelTaskFunc func(taskID string) error

// RetryTaskFunc requeues a task by ID. It is responsible for all validation,
// state changes, and event emission.
type RetryTaskFunc func(taskID string) error

type discordTaskStore interface {
	GetTask(ctx context.Context, id string) (*models.Task, error)
	ListTasks(ctx context.Context, filter store.TaskFilter) ([]*models.Task, error)
}

// HandlerActions groups the callback functions and authorization config for
// the Discord interaction handler. All fields are optional; nil callbacks
// disable the corresponding action.
type HandlerActions struct {
	CreateTask   CreateTaskFunc
	CancelTask   CancelTaskFunc
	RetryTask    RetryTaskFunc
	AllowedRoles []string
	CommandName  string
}

// InteractionHandler returns an http.HandlerFunc that verifies and routes
// Discord interaction webhook requests.
func InteractionHandler(publicKey ed25519.PublicKey, taskStore discordTaskStore, actions HandlerActions) http.HandlerFunc {
	if actions.CommandName == "" {
		actions.CommandName = "backflow"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		signature := r.Header.Get("X-Signature-Ed25519")
		timestamp := r.Header.Get("X-Signature-Timestamp")

		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			log.Warn().Err(err).Msg("discord: failed to read request body")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		log.Debug().
			Str("signature", signature).
			Str("timestamp", timestamp).
			Int("body_len", len(body)).
			Msg("discord: incoming interaction")

		if !verifySignature(publicKey, signature, timestamp, body) {
			log.Warn().Msg("discord: signature verification failed")
			http.Error(w, "invalid request signature", http.StatusUnauthorized)
			return
		}

		var interaction Interaction
		if err := json.Unmarshal(body, &interaction); err != nil {
			log.Warn().Err(err).Msg("discord: invalid interaction JSON")
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}

		switch interaction.Type {
		case InteractionTypePing:
			log.Info().Msg("discord: PING received, responding with PONG")
			respondJSON(w, InteractionResponse{Type: ResponseTypePong})
		case InteractionTypeApplicationCommand:
			handleApplicationCommand(r.Context(), w, interaction, taskStore, actions)
		case InteractionTypeModalSubmit:
			handleModalSubmit(r.Context(), w, interaction, actions.CreateTask)
		case InteractionTypeMessageComponent:
			handleMessageComponent(r.Context(), w, interaction, actions)
		default:
			log.Warn().Int("type", interaction.Type).Msg("discord: unknown interaction type")
			http.Error(w, "unknown interaction type", http.StatusBadRequest)
		}
	}
}

func handleApplicationCommand(ctx context.Context, w http.ResponseWriter, interaction Interaction, taskStore discordTaskStore, actions HandlerActions) {
	var cmd CommandData
	if err := json.Unmarshal(interaction.Data, &cmd); err != nil {
		log.Warn().Err(err).Msg("discord: failed to parse command data")
		http.Error(w, "invalid command data", http.StatusBadRequest)
		return
	}

	log.Info().Str("command", cmd.Name).Msg("discord: application command received")

	if cmd.Name != actions.CommandName {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Unknown command: %s", cmd.Name)},
		})
		return
	}

	subcommand, options, ok := cmd.firstSubcommand()
	if !ok {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Use %s.", subcommandsHelp(actions.CommandName))},
		})
		return
	}

	switch subcommand {
	case "create":
		openCreateModal(w)
	case "status":
		taskID, err := stringOption(options, "task_id")
		if err != nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: err.Error()},
			})
			return
		}
		if taskStore == nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Task lookup is unavailable right now."},
			})
			return
		}
		task, err := taskStore.GetTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				respondJSON(w, ChannelMessageResponse{
					Type: ResponseTypeChannelMessage,
					Data: MessageData{Content: fmt.Sprintf("Task %s not found.", taskID)},
				})
				return
			}
			log.Warn().Err(err).Str("task_id", taskID).Msg("discord: failed to load task")
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Failed to load task status."},
			})
			return
		}
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: formatTaskStatus(task)},
		})
	case "list":
		if taskStore == nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Task lookup is unavailable right now."},
			})
			return
		}
		filter := store.TaskFilter{Limit: defaultDiscordTaskListLimit}
		if statusValue, err := stringOption(options, "status"); err == nil && statusValue != "" {
			status := models.TaskStatus(statusValue)
			filter.Status = &status
		}
		if limit, err := intOption(options, "limit"); err == nil && limit > 0 {
			if limit > maxDiscordTaskListLimit {
				limit = maxDiscordTaskListLimit
			}
			filter.Limit = limit
		}
		if offset, err := intOption(options, "offset"); err == nil && offset >= 0 {
			filter.Offset = offset
		}

		tasks, err := taskStore.ListTasks(ctx, filter)
		if err != nil {
			log.Warn().Err(err).Msg("discord: failed to list tasks")
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Failed to list tasks."},
			})
			return
		}
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: formatTaskList(tasks, filter)},
		})
	case "cancel":
		if !hasPermission(interaction.Member, actions.AllowedRoles) {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "You don't have permission to cancel tasks.", Flags: FlagEphemeral},
			})
			return
		}
		taskID, err := stringOption(options, "task_id")
		if err != nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: err.Error(), Flags: FlagEphemeral},
			})
			return
		}
		if actions.CancelTask == nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Task cancellation is unavailable right now.", Flags: FlagEphemeral},
			})
			return
		}
		if err := actions.CancelTask(taskID); err != nil {
			log.Warn().Err(err).Str("task_id", taskID).Msg("discord: failed to cancel task")
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: fmt.Sprintf("Failed to cancel task %s: %s", taskID, err.Error()), Flags: FlagEphemeral},
			})
			return
		}
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Task %s has been cancelled.", taskID), Flags: FlagEphemeral},
		})
	case "read":
		handleReadCommand(ctx, w, interaction, options, actions)
	case "retry":
		if !hasPermission(interaction.Member, actions.AllowedRoles) {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "You don't have permission to retry tasks.", Flags: FlagEphemeral},
			})
			return
		}
		taskID, err := stringOption(options, "task_id")
		if err != nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: err.Error(), Flags: FlagEphemeral},
			})
			return
		}
		if actions.RetryTask == nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Task retry is unavailable right now.", Flags: FlagEphemeral},
			})
			return
		}
		if err := actions.RetryTask(taskID); err != nil {
			log.Warn().Err(err).Str("task_id", taskID).Msg("discord: failed to retry task")
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: fmt.Sprintf("Failed to retry task %s: %s", taskID, err.Error()), Flags: FlagEphemeral},
			})
			return
		}
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Task %s has been queued for retry.", taskID), Flags: FlagEphemeral},
		})
	default:
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Unknown subcommand: %s. Use %s.", subcommand, subcommandsHelp(actions.CommandName))},
		})
	}
}

func handleModalSubmit(ctx context.Context, w http.ResponseWriter, interaction Interaction, createTask CreateTaskFunc) {
	var data ModalSubmitData
	if err := json.Unmarshal(interaction.Data, &data); err != nil {
		log.Warn().Err(err).Msg("discord: failed to parse modal submit data")
		http.Error(w, "invalid modal data", http.StatusBadRequest)
		return
	}

	log.Info().Str("custom_id", data.CustomID).Msg("discord: modal submit received")

	if data.CustomID == modalIDCreate {
		handleCreateSubmit(ctx, w, data, createTask)
		return
	}

	log.Warn().Str("custom_id", data.CustomID).Msg("discord: unknown modal custom_id")
	respondJSON(w, ChannelMessageResponse{
		Type: ResponseTypeChannelMessage,
		Data: MessageData{Content: "Unknown modal submission."},
	})
}

func handleMessageComponent(ctx context.Context, w http.ResponseWriter, interaction Interaction, actions HandlerActions) {
	var data ComponentData
	if err := json.Unmarshal(interaction.Data, &data); err != nil {
		log.Warn().Err(err).Msg("discord: failed to parse component data")
		http.Error(w, "invalid component data", http.StatusBadRequest)
		return
	}

	log.Info().Str("custom_id", data.CustomID).Msg("discord: message component received")

	if !hasPermission(interaction.Member, actions.AllowedRoles) {
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: "You don't have permission to perform this action.", Flags: FlagEphemeral},
		})
		return
	}

	switch {
	case strings.HasPrefix(data.CustomID, CustomIDCancelPrefix):
		taskID := strings.TrimPrefix(data.CustomID, CustomIDCancelPrefix)
		if actions.CancelTask == nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Task cancellation is unavailable right now.", Flags: FlagEphemeral},
			})
			return
		}
		if err := actions.CancelTask(taskID); err != nil {
			log.Warn().Err(err).Str("task_id", taskID).Msg("discord: button cancel failed")
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: fmt.Sprintf("Failed to cancel task %s: %s", taskID, err.Error()), Flags: FlagEphemeral},
			})
			return
		}
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Task %s has been cancelled.", taskID), Flags: FlagEphemeral},
		})
	case strings.HasPrefix(data.CustomID, CustomIDRetryPrefix):
		taskID := strings.TrimPrefix(data.CustomID, CustomIDRetryPrefix)
		if actions.RetryTask == nil {
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: "Task retry is unavailable right now.", Flags: FlagEphemeral},
			})
			return
		}
		if err := actions.RetryTask(taskID); err != nil {
			log.Warn().Err(err).Str("task_id", taskID).Msg("discord: button retry failed")
			respondJSON(w, ChannelMessageResponse{
				Type: ResponseTypeChannelMessage,
				Data: MessageData{Content: fmt.Sprintf("Failed to retry task %s: %s", taskID, err.Error()), Flags: FlagEphemeral},
			})
			return
		}
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Task %s has been queued for retry.", taskID), Flags: FlagEphemeral},
		})
	default:
		log.Warn().Str("custom_id", data.CustomID).Msg("discord: unknown message component custom_id")
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: "Unknown button action.", Flags: FlagEphemeral},
		})
	}
}

// hasPermission returns true if no roles are configured (open access) or the
// member holds at least one of the allowed roles.
func hasPermission(member *MemberInfo, allowedRoles []string) bool {
	if len(allowedRoles) == 0 {
		return true
	}
	if member == nil {
		return false
	}
	for _, allowed := range allowedRoles {
		for _, memberRole := range member.Roles {
			if allowed == memberRole {
				return true
			}
		}
	}
	return false
}

func (c CommandData) firstSubcommand() (string, []CommandOption, bool) {
	for _, opt := range c.Options {
		if opt.Type == 1 {
			return opt.Name, opt.Options, true
		}
	}
	return "", nil, false
}

func stringOption(options []CommandOption, name string) (string, error) {
	for _, opt := range options {
		if opt.Name != name {
			continue
		}
		var s string
		if err := json.Unmarshal(opt.Value, &s); err != nil {
			return "", fmt.Errorf("invalid %s option", name)
		}
		return s, nil
	}
	return "", fmt.Errorf("missing required option: %s", name)
}

func intOption(options []CommandOption, name string) (int, error) {
	for _, opt := range options {
		if opt.Name != name {
			continue
		}
		var n int
		if err := json.Unmarshal(opt.Value, &n); err != nil {
			return 0, fmt.Errorf("invalid %s option", name)
		}
		return n, nil
	}
	return 0, fmt.Errorf("missing required option: %s", name)
}

// subcommandsHelp renders the list of /backflow subcommands as the
// human-readable fragment used inside "Use ..." messages — e.g.
// "/backflow create, /backflow status, ..., or /backflow read".
func subcommandsHelp(cmdName string) string {
	subs := []string{"create", "status", "list", "cancel", "retry", "read"}
	parts := make([]string, len(subs))
	for i, s := range subs {
		parts[i] = "/" + cmdName + " " + s
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", or " + parts[len(parts)-1]
}

func boolOption(options []CommandOption, name string) (value, ok bool) {
	for _, opt := range options {
		if opt.Name != name {
			continue
		}
		var b bool
		if err := json.Unmarshal(opt.Value, &b); err != nil {
			return false, false
		}
		return b, true
	}
	return false, false
}

const (
	defaultDiscordTaskListLimit = 5
	maxDiscordTaskListLimit     = 10
	maxDiscordMessageLength     = 1900
)

func formatTaskStatus(task *models.Task) string {
	parts := []string{
		fmt.Sprintf("Task %s is %s.", task.ID, task.Status),
	}
	if task.RepoURL != "" {
		parts = append(parts, task.RepoURL)
	}
	if task.PRURL != "" {
		parts = append(parts, task.PRURL)
	}
	if task.CompletedAt != nil {
		parts = append(parts, "completed "+task.CompletedAt.UTC().Format(time.RFC3339))
	}
	if task.StartedAt != nil && task.CompletedAt == nil {
		parts = append(parts, "started "+task.StartedAt.UTC().Format(time.RFC3339))
	}
	content := strings.Join(parts, " | ")
	return truncate(content, maxDiscordMessageLength)
}

func formatTaskList(tasks []*models.Task, filter store.TaskFilter) string {
	if len(tasks) == 0 {
		if filter.Status != nil {
			return fmt.Sprintf("No tasks found for status %s.", *filter.Status)
		}
		return "No tasks found."
	}

	var b strings.Builder
	header := fmt.Sprintf("Tasks (%d shown", len(tasks))
	if filter.Offset > 0 {
		header += fmt.Sprintf(", offset %d", filter.Offset)
	}
	if filter.Status != nil {
		header += fmt.Sprintf(", status %s", *filter.Status)
	}
	header += "):"
	b.WriteString(header)
	for _, task := range tasks {
		line := fmt.Sprintf("\n- %s | %s", task.ID, task.Status)
		if task.RepoURL != "" {
			line += " | " + task.RepoURL
		}
		if b.Len()+len(line) > maxDiscordMessageLength {
			b.WriteString("\n- ...")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-3]) + "..."
}

func verifySignature(publicKey ed25519.PublicKey, signatureHex, timestamp string, body []byte) bool {
	if signatureHex == "" || timestamp == "" {
		return false
	}
	sig, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	msg := append([]byte(timestamp), body...)
	return ed25519.Verify(publicKey, msg, sig)
}

func respondJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Warn().Err(err).Msg("discord: failed to encode response")
	}
}

// ParsePublicKey decodes a hex-encoded Ed25519 public key.
func ParsePublicKey(hexKey string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode hex: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid key length: got %d bytes, want %d", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}
