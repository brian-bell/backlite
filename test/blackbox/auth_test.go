//go:build !nocontainers

package blackbox_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestAuth_EnvToken_AllowsAndRejects(t *testing.T) {
	resetBetweenTests(t)

	baseURL, stop := startAuthBacklite(t, "env-secret", "env-secret")
	defer stop()

	if got := statusCode(t, baseURL, "/api/v1/health", ""); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/v1/health = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := statusCode(t, baseURL, "/api/v1/health", "env-secret"); got != http.StatusOK {
		t.Fatalf("authenticated GET /api/v1/health = %d, want %d", got, http.StatusOK)
	}
}

func TestAuth_DatabaseKey_AllowsAndRejects(t *testing.T) {
	resetBetweenTests(t)

	seedAPIKey(t, "db-valid", []string{"health:read"}, nil)

	baseURL, stop := startAuthBacklite(t, "", "db-valid")
	defer stop()

	if got := statusCode(t, baseURL, "/api/v1/health", ""); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /api/v1/health = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := statusCode(t, baseURL, "/api/v1/health", "db-valid"); got != http.StatusOK {
		t.Fatalf("authenticated GET /api/v1/health = %d, want %d", got, http.StatusOK)
	}
}

func TestAuth_DatabaseKey_RejectsExpired(t *testing.T) {
	resetBetweenTests(t)

	seedAPIKey(t, "db-valid", []string{"health:read"}, nil)
	expired := time.Now().Add(-time.Hour)
	seedAPIKey(t, "db-expired", []string{"health:read"}, &expired)

	baseURL, stop := startAuthBacklite(t, "", "db-valid")
	defer stop()

	if got := statusCode(t, baseURL, "/api/v1/health", "db-expired"); got != http.StatusUnauthorized {
		t.Fatalf("expired GET /api/v1/health = %d, want %d", got, http.StatusUnauthorized)
	}
}

func seedAPIKey(t *testing.T, token string, permissions []string, expiresAt *time.Time) {
	t.Helper()

	hash := sha256.Sum256([]byte(token))
	perms, err := json.Marshal(permissions)
	if err != nil {
		t.Fatalf("marshal permissions: %v", err)
	}

	ctx := context.Background()
	_, err = dbPool.ExecContext(ctx, `
		INSERT INTO api_keys (key_hash, name, permissions, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT (key_hash) DO UPDATE SET
			name = excluded.name,
			permissions = excluded.permissions,
			expires_at = excluded.expires_at,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`,
		hex.EncodeToString(hash[:]),
		"blackbox",
		string(perms),
		func() any {
			if expiresAt == nil {
				return nil
			}
			return expiresAt.UTC().Format(time.RFC3339Nano)
		}(),
	)
	if err != nil {
		t.Fatalf("seed api key: %v", err)
	}
}

func startAuthBacklite(t *testing.T, envToken, healthToken string) (string, func()) {
	t.Helper()

	port, err := freePort()
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	baseURL := fmt.Sprintf("http://localhost:%d", port)

	stderr := &syncBuffer{}
	cmd := exec.Command(backflowBinaryPath)
	cmd.Dir = repoRoot
	cmd.Stdout = nil
	cmd.Stderr = stderr
	cmd.Env = buildSubprocessEnv(port, dbPath, listener.URL())
	if envToken != "" {
		cmd.Env = append(cmd.Env, "BACKFLOW_API_KEY="+envToken)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start backlite subprocess: %v", err)
	}

	if err := waitForHealthWithToken(baseURL, healthToken, 30*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("wait for auth-enabled health: %v\nstderr:\n%s", err, stderr.String())
	}

	stop := func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGINT)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	}

	return baseURL, stop
}

func statusCode(t *testing.T, baseURL, path, token string) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
