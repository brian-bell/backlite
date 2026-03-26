package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/messaging"
)

// MessagingNotifier sends SMS notifications to task creators who submitted
// via messaging. It reads the reply channel directly from the Event.
type MessagingNotifier struct {
	messenger messaging.Messenger
	events    map[EventType]bool // nil = send all events
}

func NewMessagingNotifier(m messaging.Messenger, filterEvents []string) *MessagingNotifier {
	mn := &MessagingNotifier{
		messenger: m,
	}
	if len(filterEvents) > 0 {
		mn.events = make(map[EventType]bool, len(filterEvents))
		for _, e := range filterEvents {
			mn.events[EventType(e)] = true
		}
	}
	return mn
}

func (m *MessagingNotifier) Notify(event Event) error {
	// Check event filter
	if m.events != nil && !m.events[event.Type] {
		return nil
	}

	if event.ReplyChannel == "" {
		return nil
	}

	channel, err := parseReplyChannel(event.ReplyChannel)
	if err != nil {
		log.Warn().Err(err).Str("reply_channel", event.ReplyChannel).Msg("invalid reply channel")
		return nil
	}

	body := formatEventMessage(event)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := m.messenger.Send(ctx, messaging.OutboundMessage{
		Channel: channel,
		Body:    body,
	}); err != nil {
		log.Warn().Err(err).Str("task_id", event.TaskID).Msg("failed to send messaging notification")
		return nil
	}

	log.Debug().Str("task_id", event.TaskID).Str("event", string(event.Type)).Msg("messaging notification sent")
	return nil
}

func (m *MessagingNotifier) Name() string { return "sms" }

// parseReplyChannel converts "sms:+15551234567" into a Channel.
func parseReplyChannel(rc string) (messaging.Channel, error) {
	parts := strings.SplitN(rc, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return messaging.Channel{}, fmt.Errorf("invalid reply channel format: %q", rc)
	}
	return messaging.Channel{
		Type:    messaging.ChannelType(parts[0]),
		Address: parts[1],
	}, nil
}

// formatEventMessage returns a concise, human-readable status message.
func formatEventMessage(event Event) string {
	switch event.Type {
	case EventTaskCompleted:
		msg := fmt.Sprintf("Task %s completed.", event.TaskID)
		if event.PRURL != "" {
			msg += fmt.Sprintf("\nPR: %s", event.PRURL)
		}
		return msg
	case EventTaskFailed:
		msg := fmt.Sprintf("Task %s failed.", event.TaskID)
		if event.Message != "" {
			msg += fmt.Sprintf("\n%s", truncate(event.Message, 100))
		}
		return msg
	case EventTaskRunning:
		return fmt.Sprintf("Task %s is now running.", event.TaskID)
	case EventTaskInterrupted:
		return fmt.Sprintf("Task %s was interrupted and will be retried.", event.TaskID)
	case EventTaskRecovering:
		return fmt.Sprintf("Task %s is recovering.", event.TaskID)
	case EventTaskCancelled:
		return fmt.Sprintf("Task %s was cancelled.", event.TaskID)
	case EventTaskRetry:
		return fmt.Sprintf("Task %s has been queued for retry.", event.TaskID)
	default:
		return fmt.Sprintf("Task %s: %s", event.TaskID, event.Type)
	}
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
