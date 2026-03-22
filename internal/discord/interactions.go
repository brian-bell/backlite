package discord

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog/log"
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

// Interaction is the minimal Discord interaction payload needed for routing.
type Interaction struct {
	Type int             `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// CommandData contains the parsed command name from an application command interaction.
type CommandData struct {
	Name string `json:"name"`
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

// MessageData is the content payload inside a channel message response.
type MessageData struct {
	Content string `json:"content"`
}

// InteractionHandler returns an http.HandlerFunc that verifies and routes
// Discord interaction webhook requests.
func InteractionHandler(publicKey ed25519.PublicKey) http.HandlerFunc {
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
			handleApplicationCommand(w, interaction)
		case InteractionTypeMessageComponent, InteractionTypeModalSubmit:
			log.Info().Int("type", interaction.Type).Msg("discord: interaction received (stub)")
			respondJSON(w, InteractionResponse{Type: ResponseTypeDeferredChannelMessage})
		default:
			log.Warn().Int("type", interaction.Type).Msg("discord: unknown interaction type")
			http.Error(w, "unknown interaction type", http.StatusBadRequest)
		}
	}
}

func handleApplicationCommand(w http.ResponseWriter, interaction Interaction) {
	var cmd CommandData
	if err := json.Unmarshal(interaction.Data, &cmd); err != nil {
		log.Warn().Err(err).Msg("discord: failed to parse command data")
		http.Error(w, "invalid command data", http.StatusBadRequest)
		return
	}

	log.Info().Str("command", cmd.Name).Msg("discord: application command received")

	switch cmd.Name {
	case "backflow":
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: "Backflow is running."},
		})
	default:
		respondJSON(w, ChannelMessageResponse{
			Type: ResponseTypeChannelMessage,
			Data: MessageData{Content: fmt.Sprintf("Unknown command: %s", cmd.Name)},
		})
	}
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
