package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/brian-bell/backlite/internal/store"
)

const hasKeysCacheTTL = 30 * time.Second

// AuthMiddleware returns chi-compatible middleware that enforces bearer-token
// authentication. When expectedToken is set (BACKFLOW_API_KEY), it acts as a
// simple all-or-nothing gate. Otherwise it falls through to DB-backed api_keys
// lookup with scoped permissions.
func AuthMiddleware(s store.Store, expectedToken string) func(http.Handler) http.Handler {
	if expectedToken == "" && s == nil {
		return func(next http.Handler) http.Handler { return next }
	}

	var (
		cacheMu     sync.Mutex
		cachedHas   bool
		cachedAt    time.Time
		expectedBuf []byte
	)
	if expectedToken != "" {
		expectedBuf = []byte(expectedToken)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := parseBearerToken(r.Header.Get("Authorization"))
			if expectedToken != "" {
				if !ok || subtle.ConstantTimeCompare([]byte(token), expectedBuf) != 1 {
					writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if s == nil {
				next.ServeHTTP(w, r)
				return
			}

			hasKeys, err := cachedHasAPIKeys(r.Context(), s, &cacheMu, &cachedHas, &cachedAt)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to check API key configuration")
				return
			}
			if !hasKeys {
				next.ServeHTTP(w, r)
				return
			}

			if !ok {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}

			keyHash := sha256.Sum256([]byte(token))
			apiKey, err := s.GetAPIKeyByHash(r.Context(), hex.EncodeToString(keyHash[:]))
			if err != nil {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
			if apiKey == nil || apiKey.Expired(time.Now().UTC()) {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}

			requiredScope, ok := requiredScopeForRequest(r)
			if !ok {
				writeError(w, http.StatusForbidden, "API key does not have permission for this route")
				return
			}
			if !apiKey.HasPermission(requiredScope) {
				writeError(w, http.StatusForbidden, "API key does not have permission for this route")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func cachedHasAPIKeys(ctx context.Context, s store.Store, mu *sync.Mutex, cached *bool, cachedAt *time.Time) (bool, error) {
	mu.Lock()
	if !cachedAt.IsZero() && time.Since(*cachedAt) < hasKeysCacheTTL {
		v := *cached
		mu.Unlock()
		return v, nil
	}
	mu.Unlock()

	hasKeys, err := s.HasAPIKeys(ctx)
	if err != nil {
		return false, err
	}

	mu.Lock()
	*cached = hasKeys
	*cachedAt = time.Now()
	mu.Unlock()

	return hasKeys, nil
}

func requiredScopeForRequest(r *http.Request) (string, bool) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
		return "health:read", true
	case r.Method == http.MethodGet && r.URL.Path == "/debug/stats":
		return "stats:read", true
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/tasks":
		return "tasks:read", true
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/tasks":
		return "tasks:write", true
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/tasks/") && !strings.HasSuffix(r.URL.Path, "/logs"):
		return "tasks:read", true
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs"):
		return "tasks:read", true
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/output"):
		return "tasks:read", true
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/output.json"):
		return "tasks:read", true
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
		return "tasks:write", true
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/retry"):
		return "tasks:write", true
	default:
		return "", false
	}
}

func parseBearerToken(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", false
	}
	return token, true
}
