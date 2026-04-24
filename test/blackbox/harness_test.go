//go:build !nocontainers

package blackbox_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/brian-bell/backlite/internal/store"
)

// Shared state initialized by TestMain and used by all tests.
var (
	backflowURL        string
	backflowBinaryPath string
	client             *BackliteClient
	listener           *WebhookListener
	dbPool             *sql.DB
	dbPath             string
	backflowCmd        *exec.Cmd
	stderrBuf          *syncBuffer
	repoRoot           string
)

// syncBuffer is a thread-safe bytes.Buffer for capturing subprocess stderr
// while the test process reads it concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sb *syncBuffer) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

func TestMain(m *testing.M) {
	// Determine repo root from this file's location: test/blackbox/ → repo root
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "cannot determine test file path")
		os.Exit(1)
	}
	repoRoot = filepath.Join(filepath.Dir(thisFile), "..", "..")

	ctx := context.Background()

	// --- Step 1: Build the Backlite binary ---
	binaryPath := filepath.Join(repoRoot, "bin", "backlite-test")
	backflowBinaryPath = binaryPath
	fmt.Println("==> Building Backlite binary...")
	build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./cmd/backlite")
	build.Dir = repoRoot
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build binary: %v\n", err)
		os.Exit(1)
	}

	// --- Step 2: Build the fake agent Docker image ---
	fakeAgentDir := filepath.Join(repoRoot, "test", "blackbox", "fake-agent")
	fmt.Println("==> Building fake agent Docker image...")
	dockerBuild := exec.Command("docker", "build", "-t", "backlite-fake-agent:test", fakeAgentDir)
	dockerBuild.Stdout = os.Stdout
	dockerBuild.Stderr = os.Stderr
	if err := dockerBuild.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build fake agent image: %v\n", err)
		os.Exit(1)
	}

	// --- Step 3: Create and migrate the SQLite test DB ---
	dbPath = filepath.Join(repoRoot, "test", "blackbox", "backlite-blackbox-test.db")
	_ = os.Remove(dbPath)
	bootstrapStore, err := store.NewSQLite(ctx, dbPath, filepath.Join(repoRoot, "migrations"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "create sqlite store: %v\n", err)
		os.Exit(1)
	}
	if err := bootstrapStore.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close bootstrap store: %v\n", err)
		os.Exit(1)
	}

	dbPool, err = sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open sqlite db: %v\n", err)
		os.Exit(1)
	}
	if _, err := dbPool.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		fmt.Fprintf(os.Stderr, "enable foreign keys: %v\n", err)
		os.Exit(1)
	}

	// --- Step 4: Start webhook listener ---
	fmt.Println("==> Starting webhook listener...")
	listener = newWebhookListener()

	// --- Step 5: Find a free port for Backlite ---
	port, err := freePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "find free port: %v\n", err)
		os.Exit(1)
	}
	backflowURL = fmt.Sprintf("http://localhost:%d", port)

	// --- Step 6: Start the Backlite subprocess ---
	fmt.Printf("==> Starting Backlite subprocess on :%d...\n", port)
	stderrBuf = &syncBuffer{}

	backflowCmd = exec.Command(binaryPath)
	backflowCmd.Dir = repoRoot
	backflowCmd.Stdout = os.Stdout
	backflowCmd.Stderr = stderrBuf
	backflowCmd.Env = buildSubprocessEnv(port, dbPath, listener.URL())

	if err := backflowCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start backlite subprocess: %v\n", err)
		os.Exit(1)
	}

	// --- Step 7: Create client and wait for health ---
	client = newBackliteClient(backflowURL)

	fmt.Println("==> Waiting for Backlite health check...")
	if err := waitForHealth(backflowURL, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "health check failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "--- Backlite stderr ---")
		fmt.Fprintln(os.Stderr, stderrBuf.String())
		fmt.Fprintln(os.Stderr, "--- end ---")
		backflowCmd.Process.Kill()
		os.Exit(1)
	}

	fmt.Println("==> Backlite is ready, running tests...")

	// --- Run tests ---
	code := m.Run()

	// --- Teardown ---
	fmt.Println("==> Shutting down...")
	backflowCmd.Process.Signal(syscall.SIGINT)

	// Give it up to 10 seconds for graceful shutdown.
	done := make(chan error, 1)
	go func() { done <- backflowCmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		backflowCmd.Process.Kill()
	}

	listener.Close()
	dbPool.Close()
	_ = os.Remove(dbPath)

	// Clean up test binary.
	os.Remove(binaryPath)

	os.Exit(code)
}

// buildSubprocessEnv constructs a clean environment for the Backlite subprocess,
// avoiding interference from inherited env vars (e.g., BACKFLOW_DISCORD_APP_ID).
func buildSubprocessEnv(port int, dbPath, webhookURL string) []string {
	env := []string{
		"BACKFLOW_POLL_INTERVAL_SEC=1",
		"BACKFLOW_AGENT_IMAGE=backlite-fake-agent:test",
		fmt.Sprintf("BACKFLOW_LISTEN_ADDR=:%d", port),
		fmt.Sprintf("BACKFLOW_DATABASE_PATH=%s", dbPath),
		fmt.Sprintf("BACKFLOW_WEBHOOK_URL=%s", webhookURL),
		"ANTHROPIC_API_KEY=sk-test-fake",
		"BACKFLOW_CONTAINER_CPUS=1",
		"BACKFLOW_CONTAINER_MEMORY_GB=1",
		"BACKFLOW_CONTAINERS_PER_INSTANCE=1",
		"BACKFLOW_DEFAULT_SAVE_AGENT_OUTPUT=false",
		"BACKFLOW_DEFAULT_CREATE_PR=false",
		"BACKFLOW_DEFAULT_SELF_REVIEW=false",
	}

	// Pass through essential system env vars.
	for _, key := range []string{"PATH", "HOME", "USER", "DOCKER_HOST", "TMPDIR"} {
		if v := os.Getenv(key); v != "" {
			env = append(env, key+"="+v)
		}
	}

	return env
}

// freePort asks the OS for an available TCP port.
// Note: there is a small TOCTOU window between Close() and the subprocess
// binding to the port, where another process could claim it. In practice
// this is extremely unlikely in CI/test environments.
func freePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// waitForHealth polls the /api/v1/health endpoint until it returns 200 or the
// timeout expires.
func waitForHealth(baseURL string, timeout time.Duration) error {
	return waitForHealthWithToken(baseURL, "", timeout)
}

func waitForHealthWithToken(baseURL, token string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	httpClient := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/health", nil)
		if err != nil {
			return err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("health endpoint did not become ready within %s", timeout)
}

// resetBetweenTests truncates all tables, re-creates the synthetic local
// instance, and resets the webhook listener. Call at the start of each test.
func resetBetweenTests(t *testing.T) {
	t.Helper()

	waitForOrchestratorIdle(t, 30*time.Second)

	ctx := context.Background()

	// Truncate all tables. This removes any state from previous tests.
	// NOTE: Keep this list in sync with migrations — add new tables here when
	// new migrations introduce them.
	_, err := dbPool.ExecContext(ctx, `
		DELETE FROM readings;
		DELETE FROM api_keys;
		DELETE FROM tasks;
		DELETE FROM instances;`)
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}

	// Re-create the synthetic local instance. The orchestrator's initInstance()
	// only runs at startup, so after truncation we must re-insert it manually.
	_, err = dbPool.ExecContext(ctx, `
		INSERT INTO instances (instance_id, status, max_containers, running_containers, created_at, updated_at)
		VALUES ('local', 'running', 1, 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT (instance_id) DO UPDATE
		SET status = 'running', running_containers = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`)
	if err != nil {
		t.Fatalf("re-create local instance: %v", err)
	}

	listener.Reset()
}

// waitForOrchestratorIdle waits until there are no non-terminal tasks left and
// the synthetic local instance has no running containers.
func waitForOrchestratorIdle(t *testing.T, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	ctx := context.Background()

	for {
		activeTasks, runningContainers, err := orchestratorState(ctx)
		if err != nil {
			t.Fatalf("check orchestrator idle state: %v", err)
		}
		if activeTasks == 0 && runningContainers == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("orchestrator did not go idle within %s: %d active tasks, local running_containers=%d",
				timeout, activeTasks, runningContainers)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// orchestratorState returns the number of non-terminal tasks and the synthetic
// local instance's running container count.
func orchestratorState(ctx context.Context) (int, int, error) {
	rows, err := dbPool.QueryContext(ctx, "SELECT id, status FROM tasks ORDER BY created_at ASC")
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()

	activeTasks := 0
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return 0, 0, err
		}
		if !isTerminal(status) {
			activeTasks++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	var runningContainers int
	if err := dbPool.QueryRowContext(ctx, "SELECT running_containers FROM instances WHERE instance_id = 'local'").Scan(&runningContainers); err != nil {
		return 0, 0, err
	}

	return activeTasks, runningContainers, nil
}

// dumpLogsOnFailure returns a cleanup function that dumps the Backlite
// subprocess stderr if the test failed. Register via t.Cleanup.
func dumpLogsOnFailure(t *testing.T) func() {
	return func() {
		if t.Failed() {
			t.Logf("--- Backlite subprocess stderr ---\n%s\n--- end ---", stderrBuf.String())
		}
	}
}
