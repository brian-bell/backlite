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
func (m *mockStore) WithTx(_ context.Context, fn func(store.Store) error) error { return fn(m) }
func (m *mockStore) Close() error                                               { return nil }

func newTestConfig() *config.Config {
	return &config.Config{
		DefaultHarness:     "claude_code",
		DefaultClaudeModel: "claude-sonnet-4-6",
		DefaultEffort:      "medium",
	}
}

func postForm(handler http.HandlerFunc, values url.Values) *httptest.ResponseRecorder {
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
				DefaultRepo: "https://github.com/backflow-labs/backflow",
				Enabled:     true,
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
	if task.RepoURL != "https://github.com/backflow-labs/backflow" {
		t.Errorf("repo = %q, want default", task.RepoURL)
	}
	if task.Prompt != "Fix the flaky test" {
		t.Errorf("prompt = %q", task.Prompt)
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
		"Body": {"github.com/org/repo Fix the bug"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
	if db.tasks[0].RepoURL != "https://github.com/org/repo" {
		t.Errorf("repo = %q", db.tasks[0].RepoURL)
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

func TestInboundHandler_AutoDetectsReviewMode(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",
				DefaultRepo: "https://github.com/backflow-labs/backflow",
				Enabled:     true,
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
	if task.TaskMode != models.TaskModeReview {
		t.Errorf("task_mode = %q, want %q", task.TaskMode, models.TaskModeReview)
	}
	if task.ReviewPRURL != "https://github.com/backflow-labs/backflow/pull/115" {
		t.Errorf("review_pr_url = %q", task.ReviewPRURL)
	}
	if task.RepoURL != "https://github.com/backflow-labs/backflow" {
		t.Errorf("repo_url = %q", task.RepoURL)
	}
	if task.ReviewPRNumber != 115 {
		t.Errorf("review_pr_number = %d, want 115", task.ReviewPRNumber)
	}
	if task.CreatePR {
		t.Error("expected create_pr false for review mode")
	}
}

func TestInboundHandler_ReviewModePRURLOnly(t *testing.T) {
	db := &mockStore{
		senders: map[string]*models.AllowedSender{
			"sms:+15551234567": {
				ChannelType: "sms",
				Address:     "+15551234567",
				DefaultRepo: "https://github.com/backflow-labs/backflow",
				Enabled:     true,
			},
		},
	}
	handler := InboundHandler(db, newTestConfig(), NoopMessenger{})

	// SMS body is just a PR URL with no additional prompt text.
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
	if task.TaskMode != models.TaskModeReview {
		t.Errorf("task_mode = %q, want %q", task.TaskMode, models.TaskModeReview)
	}
	if task.ReviewPRURL != "https://github.com/backflow-labs/backflow/pull/115" {
		t.Errorf("review_pr_url = %q", task.ReviewPRURL)
	}
	if task.RepoURL != "https://github.com/backflow-labs/backflow" {
		t.Errorf("repo_url = %q, want https://github.com/backflow-labs/backflow", task.RepoURL)
	}
	if task.Prompt == "" {
		t.Error("expected a default prompt for review mode")
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
				DefaultRepo: "https://github.com/backflow-labs/backflow",
				Enabled:     true,
			},
		},
	}
	cfg := newTestConfig()
	cfg.TwilioAuthToken = "test-auth-token"
	handler := InboundHandler(db, cfg, NoopMessenger{})

	// Request with no signature header
	w := postForm(handler, url.Values{
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
				DefaultRepo: "https://github.com/backflow-labs/backflow",
				Enabled:     true,
			},
		},
	}
	cfg := newTestConfig()
	cfg.TwilioAuthToken = "test-auth-token"
	handler := InboundHandler(db, cfg, NoopMessenger{})

	params := url.Values{
		"From": {"+15551234567"},
		"Body": {"Fix the test"},
	}

	// Compute valid signature for the URL the test request will hit
	reqURL := "http://example.com/webhooks/sms/inbound"
	sig := signRequest(cfg.TwilioAuthToken, reqURL, params)

	body := params.Encode()
	req := httptest.NewRequest(http.MethodPost, reqURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", sig)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if len(db.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(db.tasks))
	}
}
