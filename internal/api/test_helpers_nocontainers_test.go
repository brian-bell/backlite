//go:build nocontainers

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func checkResponse(t *testing.T, _ *http.Request, _ *httptest.ResponseRecorder) {
	t.Helper()
}

func extractID(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response JSON: %v", err)
	}
	if resp.Data.ID == "" {
		t.Fatalf("response did not contain task id: %s", string(body))
	}
	return resp.Data.ID
}
