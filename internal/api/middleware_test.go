package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

func TestRestrictAPI_BlocksAllAPIEndpoints(t *testing.T) {
	cfg := &config.Config{RestrictAPI: true}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	paths := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/health"},
		{"GET", "/api/v1/tasks"},
		{"POST", "/api/v1/tasks"},
		{"GET", "/api/v1/tasks/bf_test123"},
		{"DELETE", "/api/v1/tasks/bf_test123"},
		{"GET", "/api/v1/tasks/bf_test123/logs"},
	}

	for _, tc := range paths {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s: got status %d, want %d", tc.method, tc.path, rr.Code, http.StatusForbidden)
		}

		var resp envelope
		json.NewDecoder(rr.Body).Decode(&resp)
		if resp.Error == "" {
			t.Errorf("%s %s: expected error message in response body", tc.method, tc.path)
		}
	}
}

func TestAPIAuth_RequiresBearerToken(t *testing.T) {
	cfg := &config.Config{APIKey: "test-secret"}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/tasks: got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAPIAuth_AllowsValidBearerToken(t *testing.T) {
	cfg := &config.Config{APIKey: "test-secret"}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestDiscordWebhookRoute_Removed(t *testing.T) {
	cfg := &config.Config{}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/discord", strings.NewReader(""))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("POST /webhooks/discord: got status %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestAPIAuth_KeepsRootHealthPublic(t *testing.T) {
	cfg := &config.Config{APIKey: "test-secret"}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /health: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

type apiKeyStoreMock struct {
	store.Store
	hasKeys bool
	key     *models.APIKey
}

func (m *apiKeyStoreMock) HasAPIKeys(_ context.Context) (bool, error) {
	return m.hasKeys, nil
}

func (m *apiKeyStoreMock) GetAPIKeyByHash(_ context.Context, hash string) (*models.APIKey, error) {
	if m.key == nil {
		return nil, store.ErrNotFound
	}
	want := sha256.Sum256([]byte("db-secret"))
	if hash != hex.EncodeToString(want[:]) {
		return nil, store.ErrNotFound
	}
	return m.key, nil
}

func TestAPIAuth_RequiresBearerTokenWhenDBKeysExist(t *testing.T) {
	cfg := &config.Config{}
	s := &apiKeyStoreMock{hasKeys: true}
	router := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/tasks: got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestAPIAuth_RejectsDatabaseKeyWithoutReadScope(t *testing.T) {
	cfg := &config.Config{}
	s := &apiKeyStoreMock{
		hasKeys: true,
		key: &models.APIKey{
			KeyHash:     "",
			Name:        "read-only-health",
			Permissions: []string{"health:read"},
		},
	}
	router := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	req.Header.Set("Authorization", "Bearer db-secret")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("GET /api/v1/tasks: got status %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestAPIAuth_AllowsDatabaseKeyWithReadScope(t *testing.T) {
	cfg := &config.Config{}
	s := &apiKeyStoreMock{
		hasKeys: true,
		key: &models.APIKey{
			Name:        "read-health",
			Permissions: []string{"health:read"},
		},
	}
	router := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer db-secret")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAPIAuth_AllowsDatabaseKeyWithWriteScope(t *testing.T) {
	cfg := &config.Config{}
	s := &apiKeyStoreMock{
		hasKeys: true,
		key: &models.APIKey{
			Name:        "write-tasks",
			Permissions: []string{"tasks:write"},
		},
	}
	router := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/tasks", strings.NewReader(`{"prompt":""}`))
	req.Header.Set("Authorization", "Bearer db-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/v1/tasks: got status %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestAPIAuth_AllowsDebugStatsWithStatsScope(t *testing.T) {
	cfg := &config.Config{}
	s := &apiKeyStoreMock{
		hasKeys: true,
		key: &models.APIKey{
			Name:        "stats",
			Permissions: []string{"stats:read"},
		},
	}

	next := AuthMiddleware(s, cfg.APIKey)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	req.Header.Set("Authorization", "Bearer db-secret")
	rr := httptest.NewRecorder()
	next.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("GET /debug/stats: got status %d, want %d", rr.Code, http.StatusNoContent)
	}
}

func TestAPIAuth_RejectsExpiredDatabaseBearerToken(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	cfg := &config.Config{}
	s := &apiKeyStoreMock{
		hasKeys: true,
		key: &models.APIKey{
			Name:        "expired",
			Permissions: []string{"health:read"},
			ExpiresAt:   &expired,
		},
	}
	router := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.Header.Set("Authorization", "Bearer db-secret")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/health: got status %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestRestrictAPI_RootHealthStillAccessible(t *testing.T) {
	cfg := &config.Config{RestrictAPI: true}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET /health: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestRestrictAPI_Disabled_AllowsAPIHealth(t *testing.T) {
	cfg := &config.Config{RestrictAPI: false}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/v1/health: got status %d, want %d", rr.Code, http.StatusOK)
	}
}
