package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/discord"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

const (
	discordThreadNamePrefix  = "backflow-"
	defaultThreadArchiveMins = 10080
	maxEmbedTextLength       = 1024
)

type discordThreadStore interface {
	GetDiscordTaskThread(ctx context.Context, taskID string) (*models.DiscordTaskThread, error)
	UpsertDiscordTaskThread(ctx context.Context, thread *models.DiscordTaskThread) error
}

// DiscordNotifier delivers lifecycle notifications into Discord channels and threads.
type DiscordNotifier struct {
	client    discord.Client
	store     discordThreadStore
	channelID string
	events    map[EventType]bool
}

func NewDiscordNotifier(client discord.Client, store discordThreadStore, channelID string, filterEvents []string) *DiscordNotifier {
	d := &DiscordNotifier{
		client:    client,
		store:     store,
		channelID: channelID,
	}
	if len(filterEvents) > 0 {
		d.events = make(map[EventType]bool, len(filterEvents))
		for _, e := range filterEvents {
			d.events[EventType(e)] = true
		}
	}
	return d
}

func (d *DiscordNotifier) Notify(event Event) error {
	if d.events != nil && !d.events[event.Type] {
		return nil
	}
	if d.client == nil || d.store == nil || d.channelID == "" {
		log.Warn().Str("event", string(event.Type)).Str("task_id", event.TaskID).Msg("discord: notifier is not configured")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	thread, err := d.store.GetDiscordTaskThread(ctx, event.TaskID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Warn().Err(err).Str("event", string(event.Type)).Str("task_id", event.TaskID).Msg("discord: failed to load task thread mapping")
		return nil
	}

	if thread != nil && thread.ThreadID != "" {
		return d.postThreadEvent(ctx, thread.ThreadID, event)
	}

	return d.bootstrapThread(ctx, event)
}

func (d *DiscordNotifier) Name() string { return "discord" }

func (d *DiscordNotifier) bootstrapThread(ctx context.Context, event Event) error {
	payload := discordMessagePayload(event)
	msg, err := d.client.CreateMessage(ctx, d.channelID, payload)
	if err != nil {
		log.Warn().Err(err).Str("event", string(event.Type)).Str("task_id", event.TaskID).Msg("discord: failed to create channel message")
		return nil
	}

	threadName := threadNameForTask(event.TaskID)
	thread, err := d.client.StartThreadFromMessage(ctx, d.channelID, msg.ID, discord.StartThreadPayload{
		Name:                threadName,
		AutoArchiveDuration: defaultThreadArchiveMins,
	})
	if err != nil {
		log.Warn().Err(err).Str("event", string(event.Type)).Str("task_id", event.TaskID).Str("message_id", msg.ID).Msg("discord: failed to start task thread")
		return nil
	}

	now := event.Timestamp.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	mapping := &models.DiscordTaskThread{
		TaskID:        event.TaskID,
		RootMessageID: msg.ID,
		ThreadID:      thread.ID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := d.store.UpsertDiscordTaskThread(ctx, mapping); err != nil {
		log.Warn().Err(err).Str("event", string(event.Type)).Str("task_id", event.TaskID).Str("message_id", msg.ID).Str("thread_id", thread.ID).Msg("discord: failed to persist task thread mapping")
		return nil
	}

	log.Debug().Str("event", string(event.Type)).Str("task_id", event.TaskID).Str("message_id", msg.ID).Str("thread_id", thread.ID).Msg("discord: bootstrapped task thread")
	return nil
}

func (d *DiscordNotifier) postThreadEvent(ctx context.Context, threadID string, event Event) error {
	payload := discordMessagePayload(event)
	msg, err := d.client.CreateMessage(ctx, threadID, payload)
	if err != nil {
		log.Warn().Err(err).Str("event", string(event.Type)).Str("task_id", event.TaskID).Str("thread_id", threadID).Msg("discord: failed to post task thread message")
		return nil
	}

	log.Debug().Str("event", string(event.Type)).Str("task_id", event.TaskID).Str("thread_id", threadID).Str("message_id", msg.ID).Msg("discord: posted task update")
	return nil
}

func discordMessagePayload(event Event) discord.MessagePayload {
	embed := discordEmbedForEvent(event)
	payload := discord.MessagePayload{
		Embeds: []discord.Embed{embed},
		AllowedMentions: &discord.AllowedMentions{
			Parse: []string{},
		},
	}
	if btns := buttonsForEvent(event); len(btns) > 0 {
		payload.Components = []discord.MessageActionRow{
			{Type: discord.ComponentTypeActionRow, Components: btns},
		}
	}
	return payload
}

func buttonsForEvent(event Event) []discord.Button {
	switch event.Type {
	case EventTaskCreated, EventTaskRunning, EventTaskRecovering:
		return []discord.Button{{
			Type:     discord.ComponentTypeButton,
			Style:    discord.ButtonStyleDanger,
			Label:    "Cancel",
			CustomID: discord.CustomIDCancelPrefix + event.TaskID,
		}}
	case EventTaskFailed, EventTaskInterrupted, EventTaskCancelled:
		if event.ReadyForRetry && !event.RetryLimitReached {
			return []discord.Button{{
				Type:     discord.ComponentTypeButton,
				Style:    discord.ButtonStylePrimary,
				Label:    "Retry",
				CustomID: discord.CustomIDRetryPrefix + event.TaskID,
			}}
		}
		return nil
	default:
		return nil
	}
}

func discordEmbedForEvent(event Event) discord.Embed {
	title := discordTitleForEvent(event)
	description, fields, color := discordEmbedContent(event)

	embed := discord.Embed{
		Title:       title,
		Description: description,
		Color:       color,
		Timestamp:   event.Timestamp.UTC().Format(time.RFC3339),
		Fields:      fields,
	}

	if event.Type == EventTaskCompleted && event.PRURL != "" {
		embed.URL = event.PRURL
	}

	return embed
}

func discordTitleForEvent(event Event) string {
	switch event.Type {
	case EventTaskCreated:
		return "Task created"
	case EventTaskRunning:
		return "Task running"
	case EventTaskCompleted:
		if event.TaskMode == models.TaskModeRead {
			return "Reading completed"
		}
		return "Task completed"
	case EventTaskFailed:
		if event.TaskMode == models.TaskModeRead {
			return "Reading failed"
		}
		return "Task failed"
	case EventTaskInterrupted:
		return "Task interrupted"
	case EventTaskRecovering:
		return "Task recovering"
	case EventTaskNeedsInput:
		return "Task needs input"
	case EventTaskCancelled:
		if event.ReadyForRetry || event.RetryLimitReached {
			return "Task cancelled"
		}
		return "Cancellation requested"
	case EventTaskRetry:
		return "Task retried"
	default:
		return "Task update"
	}
}

func discordEmbedContent(event Event) (string, []discord.EmbedField, int) {
	switch event.Type {
	case EventTaskCreated:
		fields := []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}
		if event.RepoURL != "" {
			fields = append(fields, discord.EmbedField{Name: "Repository", Value: event.RepoURL, Inline: false})
		}
		if event.Prompt != "" {
			fields = append(fields, discord.EmbedField{Name: "Prompt", Value: truncate(event.Prompt, maxEmbedTextLength), Inline: false})
		}
		return fmt.Sprintf("Task %s was created.", event.TaskID), fields, 0x5865F2
	case EventTaskRunning:
		return fmt.Sprintf("Task %s is now running.", event.TaskID), []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}, 0x57F287
	case EventTaskCompleted:
		if event.TaskMode == models.TaskModeRead {
			return readingCompletedContent(event)
		}
		fields := []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}
		if event.PRURL != "" {
			fields = append(fields, discord.EmbedField{Name: "Pull Request", Value: event.PRURL, Inline: false})
		}
		if event.Message != "" {
			fields = append(fields, discord.EmbedField{Name: "Summary", Value: truncate(event.Message, maxEmbedTextLength), Inline: false})
		}
		return fmt.Sprintf("Task %s completed.", event.TaskID), fields, 0x57F287
	case EventTaskFailed:
		noun := "Task"
		if event.TaskMode == models.TaskModeRead {
			noun = "Reading"
		}
		desc := fmt.Sprintf("%s %s failed.", noun, event.TaskID)
		if event.RetryLimitReached {
			desc = fmt.Sprintf("%s %s failed. Retry limit reached.", noun, event.TaskID)
		}
		fields := []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}
		if event.Message != "" {
			fields = append(fields, discord.EmbedField{Name: "Failure", Value: truncate(event.Message, maxEmbedTextLength), Inline: false})
		}
		if event.AgentLogTail != "" {
			fields = append(fields, discord.EmbedField{Name: "Log Tail", Value: truncate(event.AgentLogTail, maxEmbedTextLength), Inline: false})
		}
		return desc, fields, 0xED4245
	case EventTaskInterrupted:
		return fmt.Sprintf("Task %s was interrupted and will be retried.", event.TaskID), []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}, 0xFAA61A
	case EventTaskRecovering:
		return fmt.Sprintf("Task %s is recovering.", event.TaskID), []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}, 0x5865F2
	case EventTaskCancelled:
		desc := fmt.Sprintf("Task %s cancellation requested. Stopping container...", event.TaskID)
		if event.RetryLimitReached {
			desc = fmt.Sprintf("Task %s has been cancelled. Retry limit reached.", event.TaskID)
		} else if event.ReadyForRetry {
			desc = fmt.Sprintf("Task %s has been cancelled and is ready to retry.", event.TaskID)
		}
		return desc, []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}, 0x95A5A6
	case EventTaskNeedsInput:
		fields := []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}
		if event.Message != "" {
			fields = append(fields, discord.EmbedField{Name: "Question", Value: truncate(event.Message, maxEmbedTextLength), Inline: false})
		}
		if event.AgentLogTail != "" {
			fields = append(fields, discord.EmbedField{Name: "Log Tail", Value: truncate(event.AgentLogTail, maxEmbedTextLength), Inline: false})
		}
		return fmt.Sprintf("Task %s needs input.", event.TaskID), fields, 0xFAA61A
	case EventTaskRetry:
		return fmt.Sprintf("Task %s has been queued for retry.", event.TaskID), []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}, 0x5865F2
	default:
		return fmt.Sprintf("Task %s: %s", event.TaskID, event.Type), []discord.EmbedField{
			{Name: "Task", Value: event.TaskID, Inline: true},
		}, 0x95A5A6
	}
}

func readingCompletedContent(event Event) (string, []discord.EmbedField, int) {
	fields := []discord.EmbedField{
		{Name: "Task", Value: event.TaskID, Inline: true},
	}

	switch event.NoveltyVerdict {
	case "duplicate":
		if event.TLDR != "" {
			fields = append(fields, discord.EmbedField{Name: "TL;DR", Value: truncate(event.TLDR, maxEmbedTextLength), Inline: false})
		}
		return "Already read this — here's what you captured:", fields, 0x95A5A6

	case "nothing new":
		if event.TLDR != "" {
			fields = append(fields, discord.EmbedField{Name: "TL;DR", Value: truncate(event.TLDR, maxEmbedTextLength), Inline: false})
		}
		if len(event.Connections) > 0 {
			c := event.Connections[0]
			fields = append(fields, discord.EmbedField{Name: "Most Similar", Value: truncate(fmt.Sprintf("**%s** — %s", c.ReadingID, c.Reason), maxEmbedTextLength), Inline: false})
		}
		return "Nothing new here.", fields, 0x95A5A6

	default:
		// "new" or unrecognized verdict — full embed.
		if event.NoveltyVerdict != "" {
			fields = append(fields, discord.EmbedField{Name: "Verdict", Value: event.NoveltyVerdict, Inline: true})
		}
		if event.TLDR != "" {
			fields = append(fields, discord.EmbedField{Name: "TL;DR", Value: truncate(event.TLDR, maxEmbedTextLength), Inline: false})
		}
		if len(event.Tags) > 0 {
			fields = append(fields, discord.EmbedField{Name: "Tags", Value: strings.Join(event.Tags, ", "), Inline: false})
		}
		if len(event.Connections) > 0 {
			var lines []string
			for _, c := range event.Connections {
				lines = append(lines, fmt.Sprintf("**%s** — %s", c.ReadingID, c.Reason))
			}
			fields = append(fields, discord.EmbedField{Name: "Connections", Value: truncate(strings.Join(lines, "\n"), maxEmbedTextLength), Inline: false})
		}
		desc := "New reading saved."
		if event.Prompt != "" {
			desc = fmt.Sprintf("New reading saved: %s", event.Prompt)
		}
		return desc, fields, 0x57F287
	}
}

func threadNameForTask(taskID string) string {
	name := discordThreadNamePrefix + taskID
	if len(name) <= 100 {
		return name
	}
	return name[:100]
}

// truncate returns s bounded to maxLen runes, appending "..." when truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
