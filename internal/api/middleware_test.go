package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/store"
)

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

func TestReadingsLookup_RemainsAccessibleWhenDBKeysExist(t *testing.T) {
	cfg := &config.Config{}
	s := &apiKeyStoreMock{
		Store:   newTestStore(t),
		hasKeys: true,
	}
	router := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/readings/lookup?url="+url.QueryEscape("https://example.com/article"),
		nil,
	)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/readings/lookup: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestReadingsSimilar_RemainsAccessibleWhenDBKeysExist(t *testing.T) {
	cfg := &config.Config{}
	s := &apiKeyStoreMock{
		Store:   newTestStore(t),
		hasKeys: true,
	}
	router := NewServer(s, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/readings/similar",
		strings.NewReader(`{"query_embedding":[1,0,0],"match_count":3}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/readings/similar: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestRootHealthAccessible(t *testing.T) {
	cfg := &config.Config{}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET /health: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestAPIHealthAccessible(t *testing.T) {
	cfg := &config.Config{}
	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/v1/health: got status %d, want %d", rr.Code, http.StatusOK)
	}
}

// TestRestrictAPIEnvVar_NoLongerBlocksAPI asserts that setting
// BACKFLOW_RESTRICT_API=true in the environment does NOT cause /api/v1/*
// to be blocked. The env var was removed from config along with the
// Fly.io deployment; loading config under this env must yield a router
// that still serves /api/v1/health.
func TestRestrictAPIEnvVar_NoLongerBlocksAPI(t *testing.T) {
	t.Setenv("BACKFLOW_RESTRICT_API", "true")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("BACKFLOW_DATABASE_PATH", "/tmp/backlite-test.db")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() returned error: %v", err)
	}

	router := NewServer(&mockStore{}, cfg, noopLogFetcher{}, noopEmitter{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health with BACKFLOW_RESTRICT_API=true: got status %d, want %d", rr.Code, http.StatusOK)
	}
}
