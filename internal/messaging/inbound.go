package messaging

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

// twiMLResponse is a minimal TwiML envelope for replying to inbound SMS.
type twiMLResponse struct {
	XMLName xml.Name      `xml:"Response"`
	Message *twiMLMessage `xml:",omitempty"`
}

type twiMLMessage struct {
	XMLName xml.Name `xml:"Message"`
	Body    string   `xml:",chardata"`
}

func writeTwiML(w http.ResponseWriter, msg string) {
	resp := twiMLResponse{}
	if msg != "" {
		resp.Message = &twiMLMessage{Body: msg}
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	xml.NewEncoder(w).Encode(resp)
}

// validateTwilioSignature checks the X-Twilio-Signature header against the
// HMAC-SHA1 of the request URL + sorted POST parameters, per Twilio's spec.
func validateTwilioSignature(authToken, reqURL, signature string, params url.Values) bool {
	if signature == "" {
		return false
	}

	s := reqURL
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s += k + params.Get(k)
	}

	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(s))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expected))
}

// requestURL reconstructs the public-facing URL from the request, respecting
// X-Forwarded-Proto and X-Forwarded-Host headers set by reverse proxies.
// These headers are trusted unconditionally, so this endpoint must sit behind
// a reverse proxy that sets them, or Twilio signature validation must be
// enabled (which binds the URL to the webhook configured in Twilio's console).
func requestURL(r *http.Request) string {
	scheme := "https"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS == nil {
		scheme = "http"
	}
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	u := scheme + "://" + host + r.URL.Path
	if r.URL.RawQuery != "" {
		u += "?" + r.URL.RawQuery
	}
	return u
}

// InboundHandler returns an http.HandlerFunc that processes inbound SMS from Twilio.
func InboundHandler(db store.Store, cfg *config.Config, messenger Messenger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			log.Warn().Err(err).Msg("sms: failed to parse form")
			writeTwiML(w, "Error: could not parse request.")
			return
		}

		// Validate Twilio request signature when auth token is configured
		if cfg.TwilioAuthToken != "" {
			sig := r.Header.Get("X-Twilio-Signature")
			reqURL := requestURL(r)
			if !validateTwilioSignature(cfg.TwilioAuthToken, reqURL, sig, r.PostForm) {
				log.Warn().Str("url", reqURL).Msg("sms: invalid Twilio signature")
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}

		from := r.FormValue("From")
		body := r.FormValue("Body")

		if from == "" || body == "" {
			log.Warn().Str("from", from).Msg("sms: missing From or Body")
			writeTwiML(w, "Error: missing required fields.")
			return
		}

		log.Debug().Str("from", from).Str("body", body).Msg("sms: inbound message received")

		// Look up sender
		sender, err := db.GetAllowedSender(r.Context(), string(ChannelSMS), from)
		if errors.Is(err, store.ErrNotFound) {
			log.Warn().Str("from", from).Msg("sms: rejected message from unknown sender")
			writeTwiML(w, "Sorry, this number is not authorized to create tasks.")
			return
		}
		if err != nil {
			log.Error().Err(err).Str("from", from).Msg("sms: failed to look up sender")
			writeTwiML(w, "Error: internal error.")
			return
		}
		if !sender.Enabled {
			log.Warn().Str("from", from).Msg("sms: rejected message from unknown/disabled sender")
			writeTwiML(w, "Sorry, this number is not authorized to create tasks.")
			return
		}

		// Auto-detect review mode from the raw SMS body before URL extraction.
		taskMode := models.TaskModeCode
		reviewPRURL := ""
		reviewPRNumber := 0
		var repoURL, prompt string

		if inf := models.InferReviewMode(body); inf != nil {
			taskMode = models.TaskModeReview
			reviewPRURL = inf.PRURL
			reviewPRNumber = inf.PRNumber
			repoURL = inf.RepoURL
			// Strip the PR URL from the body to get a prompt; default if empty.
			prompt = strings.TrimSpace(strings.Replace(body, reviewPRURL, "", 1))
			if prompt == "" {
				prompt = fmt.Sprintf("Review pull request %s", reviewPRURL)
			}
		} else {
			var err error
			repoURL, prompt, err = ParseTaskFromSMS(body, sender.DefaultRepo)
			if err != nil {
				log.Warn().Err(err).Str("from", from).Msg("sms: failed to parse task")
				writeTwiML(w, fmt.Sprintf("Error: %s", err.Error()))
				return
			}
		}

		now := time.Now().UTC()
		harness := models.Harness(cfg.DefaultHarness)
		defaultModel := cfg.DefaultClaudeModel
		if harness == models.HarnessCodex {
			defaultModel = cfg.DefaultCodexModel
		}
		task := &models.Task{
			ID:              "bf_" + ulid.Make().String(),
			Status:          models.TaskStatusPending,
			TaskMode:        taskMode,
			Harness:         harness,
			RepoURL:         repoURL,
			ReviewPRURL:     reviewPRURL,
			ReviewPRNumber:  reviewPRNumber,
			Prompt:          prompt,
			Model:           defaultModel,
			Effort:          cfg.DefaultEffort,
			MaxBudgetUSD:    cfg.DefaultMaxBudget,
			MaxRuntimeMin:   int(cfg.DefaultMaxRuntime.Minutes()),
			MaxTurns:        cfg.DefaultMaxTurns,
			CreatePR:        taskMode != models.TaskModeReview,
			SelfReview:      false,
			SaveAgentOutput: true,
			ReplyChannel:    fmt.Sprintf("%s:%s", ChannelSMS, from),
			CreatedAt:       now,
			UpdatedAt:       now,
		}

		if err := db.CreateTask(r.Context(), task); err != nil {
			log.Error().Err(err).Msg("sms: failed to create task")
			writeTwiML(w, "Error: failed to create task.")
			return
		}

		log.Info().Str("task_id", task.ID).Str("from", from).Str("repo", repoURL).Msg("sms: task created")
		writeTwiML(w, fmt.Sprintf("Task created: %s\nRepo: %s", task.ID, repoURL))
	}
}
