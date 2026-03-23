package models

import "time"

// AllowedSender is a pre-registered sender authorized to create tasks via messaging.
type AllowedSender struct {
	ChannelType string    `json:"channel_type"`
	Address     string    `json:"address"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}
