//go:build !nocontainers

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSMSWebhookRoute_Removed ensures the /webhooks/sms/inbound route is not
// registered by NewServer. The SMS integration was removed; posting to this
// path must 404.
func TestSMSWebhookRoute_Removed(t *testing.T) {
	srv := testServer(t)

	req := httptest.NewRequest(http.MethodPost, "/webhooks/sms/inbound",
		strings.NewReader("From=%2B15551234567&Body=hello"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("POST /webhooks/sms/inbound: status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}
