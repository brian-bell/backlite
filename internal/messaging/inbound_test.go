package messaging

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

// --- minimal mock store ---

type mockStore struct {
	senders map[string]*models.AllowedSender
	tasks   []*models.Task
}

func (m *mockStore) GetAllowedSender(_ context.Context, channelType, address string) (*models.AllowedSender, error) {
	s, ok := m.senders[channelType+":"+address]
	if !ok {
		return nil, store.ErrNotFound
	}
	return s, nil
}

func (m *mockStore) CreateTask(_ context.Context, task *models.Task) error {
	m.tasks = append(m.tasks, task)
	return nil
}

func (m *mockStore) HasAPIKeys(context.Context) (bool, error) { return false, nil }
func (m *mockStore) GetAPIKeyByHash(context.Context, string) (*models.APIKey, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) CreateAPIKey(context.Context, *models.APIKey) error { return nil }

// Unused Store methods — satisfy the interface.
func (m *mockStore) GetTask(context.Context, string) (*models.Task, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListTasks(context.Context, store.TaskFilter) ([]*models.Task, error) {
	return nil, nil
}
func (m *mockStore) DeleteTask(context.Context, string) error               { return nil }
func (m *mockStore) CreateInstance(context.Context, *models.Instance) error { return nil }
func (m *mockStore) GetInstance(context.Context, string) (*models.Instance, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) ListInstances(context.Context, *models.InstanceStatus) ([]*models.Instance, error) {
	return nil, nil
}
func (m *mockStore) UpdateTaskStatus(context.Context, string, models.TaskStatus, string) error {
	return nil
}
func (m *mockStore) AssignTask(context.Context, string, string) error { return nil }
func (m *mockStore) StartTask(context.Context, string, string) error  { return nil }
func (m *mockStore) CompleteTask(context.Context, string, store.TaskResult) error {
	return nil
}
func (m *mockStore) RequeueTask(context.Context, string, string) error { return nil }
func (m *mockStore) CancelTask(context.Context, string) error          { return nil }
func (m *mockStore) ClearTaskAssignment(context.Context, string) error { return nil }
func (m *mockStore) MarkReadyForRetry(context.Context, string) error   { return nil }
func (m *mockStore) RetryTask(context.Context, string, int) error      { return nil }
func (m *mockStore) UpdateInstanceStatus(context.Context, string, models.InstanceStatus) error {
	return nil
}
func (m *mockStore) IncrementRunningContainers(context.Context, string) error { return nil }
func (m *mockStore) DecrementRunningContainers(context.Context, string) error { return nil }
func (m *mockStore) UpdateInstanceDetails(context.Context, string, string, string) error {
	return nil
}
func (m *mockStore) ResetRunningContainers(context.Context, string) error { return nil }
func (m *mockStore) CreateAllowedSender(context.Context, *models.AllowedSender) error {
	return nil
}
func (m *mockStore) UpsertDiscordInstall(context.Context, *models.DiscordInstall) error { return nil }
func (m *mockStore) GetDiscordInstall(context.Context, string) (*models.DiscordInstall, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) DeleteDiscordInstall(context.Context, string) error { return nil }
func (m *mockStore) UpsertDiscordTaskThread(context.Context, *models.DiscordTaskThread) error {
	return nil
}
func (m *mockStore) GetDiscordTaskThread(context.Context, string) (*models.DiscordTaskThread, error) {
	return nil, store.ErrNotFound
}
func (m *mockStore) CreateReading(context.Context, *models.Reading) error       { return nil }
func (m *mockStore) UpsertReading(context.Context, *models.Reading) error       { return nil }
func (m *mockStore) WithTx(_ context.Context, fn func(store.Store) error) error { return fn(m) }
func (m *mockStore) Close() error                                               { return nil }

const testAuthToken = "test-auth-token"

func newTestConfig() *config.Config {
	return &config.Config{
		TwilioAuthToken:    testAuthToken,
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultEffort:      "medium",
		DefaultCreatePR:    true,
		DefaultSaveOutput:  true,
	}
}

// postForm sends a signed POST to the handler, simulating a legitimate Twilio request.
func postForm(handler http.HandlerFunc, values url.Values) *httptest.ResponseRecorder {
	body := values.Encode()
	reqURL := "http://example.com/webhooks/sms/inbound"
	req := httptest.NewRequest(http.MethodPost, reqURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sig := signRequest(testAuthToken, reqURL, values)
	req.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// postUnsignedForm sends an unsigned POST, simulating a forged request.
func postUnsignedForm(handler http.HandlerFunc, values url.Values) *httptest.ResponseRecorder {
	body := values.Encode()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/sms/inbound", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestInboundHandler_AllowedSender(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",

				Enabled: true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"Fix the flaky test"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Verify task was created
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
	task := db.tasks[0]
	if task.Prompt != "Fix the flaky test" {
		t.Errorf("prompt = %q, want %q", task.Prompt, "Fix the flaky test")
	}
	if task.TaskMode != models.TaskModeAuto {
		t.Errorf("task_mode = %q, want %q", task.TaskMode, models.TaskModeAuto)
	}
	if task.RepoURL != "" {
		t.Errorf("repo_url = %q, want empty", task.RepoURL)
	}
	if task.ReplyChannel != "sms:+15551234567" {
		t.Errorf("reply_channel = %q", task.ReplyChannel)
	}
	if !task.CreatePR || task.SelfReview {
		t.Error("expected create_pr true and self_review false")
	}

	// Verify TwiML response
	var resp twiMLResponse
	if err := xml.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid TwiML: %v", err)
	}
	if resp.Message == nil {
		t.Fatal("expected TwiML message")
	}
	if !strings.Contains(resp.Message.Body, "Task created") {
		t.Errorf("unexpected response: %q", resp.Message.Body)
	}
	if strings.Contains(resp.Message.Body, "Repo:") {
		t.Errorf("response should not contain Repo line: %q", resp.Message.Body)
	}
}

func TestInboundHandler_RejectedSender(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15559999999"},
		"Body": {"hack the planet"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 0 {
		t.Fatal("expected no tasks created for rejected sender")
	}

	var resp twiMLResponse
	xml.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Message == nil || !strings.Contains(resp.Message.Body, "not authorized") {
		t.Errorf("expected rejection message, got %v", resp.Message)
	}
}

func TestInboundHandler_DisabledSender(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",
				Enabled:     false,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"Fix the test"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 0 {
		t.Fatal("expected no tasks created for disabled sender")
	}
}

func TestInboundHandler_WithExplicitRepo(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",
				Enabled:     true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"https://github.com/test/repo fix the auth bug"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
	task := db.tasks[0]
	if task.Prompt != "https://github.com/test/repo fix the auth bug" {
		t.Errorf("prompt = %q, want raw body", task.Prompt)
	}
	if task.TaskMode != models.TaskModeAuto {
		t.Errorf("task_mode = %q, want %q", task.TaskMode, models.TaskModeAuto)
	}
	if task.RepoURL != "" {
		t.Errorf("repo_url = %q, want empty", task.RepoURL)
	}
}

func TestInboundHandler_MissingFields(t *testing.T) {
	db := &mockStore{senders: map[string]*models.AllowedSender{}}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 0 {
		t.Fatal("expected no tasks created")
	}
}

func TestInboundHandler_WhitespaceOnlyBody(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",
				Enabled:     true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"   "},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 0 {
		t.Fatal("expected no tasks created for whitespace-only body")
	}
}

func TestInboundHandler_AutoDetectsReviewMode(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",

				Enabled: true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"Review https://github.com/backflow-labs/backflow/pull/115"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
	task := db.tasks[0]
	// Handler no longer auto-detects review mode; everything is "auto"
	if task.TaskMode != models.TaskModeAuto {
		t.Errorf("task_mode = %q, want %q", task.TaskMode, models.TaskModeAuto)
	}
	if task.Prompt != "Review https://github.com/backflow-labs/backflow/pull/115" {
		t.Errorf("prompt = %q, want raw body", task.Prompt)
	}
}

func TestInboundHandler_ReviewModePRURLOnly(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",

				Enabled: true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	// SMS body is just a PR URL — handler forwards it as-is, no review detection.
	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"https://github.com/backflow-labs/backflow/pull/115"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
	task := db.tasks[0]
	if task.TaskMode != models.TaskModeAuto {
		t.Errorf("task_mode = %q, want %q", task.TaskMode, models.TaskModeAuto)
	}
	if task.Prompt != "https://github.com/backflow-labs/backflow/pull/115" {
		t.Errorf("prompt = %q, want raw body", task.Prompt)
	}
}

func TestInboundHandler_TaskDefaults(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",

				Enabled: true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"Fix the flaky test"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
	task := db.tasks[0]

	// Verify defaults applied from config
	if task.Harness != "claude_code" && task.Harness != "codex" {
		t.Errorf("Harness = %q, want claude_code or codex", task.Harness)
	}
	if task.Model == "" {
		t.Error("Model is empty, want non-empty default")
	}
	if task.Effort != "medium" {
		t.Errorf("Effort = %q, want %q", task.Effort, "medium")
	}
	if !task.CreatePR {
		t.Error("CreatePR = false, want true")
	}
	if task.SelfReview {
		t.Error("SelfReview = true, want false")
	}
	if !task.SaveAgentOutput {
		t.Error("SaveAgentOutput = false, want true")
	}
}

// --- Twilio signature validation tests ---

// signRequest computes a valid X-Twilio-Signature for the given URL and params.
func signRequest(authToken, reqURL string, params url.Values) string {
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
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestValidateTwilioSignature(t *testing.T) {
	token := "test-auth-token"
	reqURL := "https://example.com/webhooks/sms/inbound"
	params := url.Values{"From": {"+15551234567"}, "Body": {"Fix the test"}}

	validSig := signRequest(token, reqURL, params)

	if !validateTwilioSignature(token, reqURL, validSig, params) {
		t.Fatal("expected valid signature to pass")
	}
	if validateTwilioSignature(token, reqURL, "invalidsig", params) {
		t.Fatal("expected invalid signature to fail")
	}
	if validateTwilioSignature(token, reqURL, "", params) {
		t.Fatal("expected empty signature to fail")
	}
	if validateTwilioSignature("wrong-token", reqURL, validSig, params) {
		t.Fatal("expected wrong token to fail")
	}
}

func TestInboundHandler_RejectsInvalidSignature(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",

				Enabled: true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	// Request with no signature header — must be rejected
	w := postUnsignedForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"Fix the test"},
	})

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if len(db.tasks) != 0 {
		t.Fatal("expected no tasks created for unsigned request")
	}
}

func TestInboundHandler_AcceptsValidSignature(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",

				Enabled: true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	// postForm signs automatically — should succeed
	w := postForm(handler, url.Values{
		"From": {"+15551234567"},
		"Body": {"Fix the test"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
}
