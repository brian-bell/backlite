//go:build !nocontainers

package fakeagent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/orchestrator"
)

const imageName = "backlite-fake-agent"

func TestMain(m *testing.M) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("cannot determine test file path")
	}
	dir := filepath.Dir(thisFile)

	cmd := exec.Command("docker", "build", "-t", imageName, dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build fake agent image: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// runFakeAgent runs the fake agent container with the given outcome and returns
// the container ID. The caller must clean up with removeContainer.
func runFakeAgent(t *testing.T, outcome string) string {
	t.Helper()
	out, err := exec.Command("docker", "run", "-d", "-e", "FAKE_OUTCOME="+outcome, imageName).Output()
	if err != nil {
		t.Fatalf("docker run failed: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// waitContainer waits for the container to exit and returns the exit code.
func waitContainer(t *testing.T, containerID string, timeout time.Duration) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "wait", containerID).Output()
	if err != nil {
		t.Fatalf("docker wait failed: %v", err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("failed to parse exit code %q: %v", string(out), err)
	}
	return code
}

// containerLogs returns stdout from the container.
func containerLogs(t *testing.T, containerID string) string {
	t.Helper()
	out, err := exec.Command("docker", "logs", containerID).Output()
	if err != nil {
		t.Fatalf("docker logs failed: %v", err)
	}
	return string(out)
}

// copyStatusJSON extracts /home/agent/workspace/status.json from the container
// into a temp file and returns the raw bytes. Returns nil if the file does not
// exist in the container.
func copyStatusJSON(t *testing.T, containerID string) []byte {
	t.Helper()
	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "status.json")

	err := exec.Command("docker", "cp", containerID+":/home/agent/workspace/status.json", dest).Run()
	if err != nil {
		return nil // file does not exist
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("failed to read copied status.json: %v", err)
	}
	return data
}

// removeContainer force-removes a container.
func removeContainer(t *testing.T, containerID string) {
	t.Helper()
	_ = exec.Command("docker", "rm", "-f", containerID).Run()
}

// parseBackliteStatusLine finds the BACKFLOW_STATUS_JSON: line in logs and
// returns the parsed JSON payload. Returns nil if no such line exists.
func parseBackliteStatusLine(t *testing.T, logs string) []byte {
	t.Helper()
	for line := range strings.SplitSeq(logs, "\n") {
		line = strings.TrimSpace(line)
		if payload, ok := strings.CutPrefix(line, "BACKFLOW_STATUS_JSON:"); ok {
			return []byte(payload)
		}
	}
	return nil
}

func TestFakeAgent(t *testing.T) {
	tests := []struct {
		name           string
		outcome        string
		wantExitCode   int
		wantStatusJSON bool
		wantComplete   bool
		wantNeedsInput bool
		wantQuestion   string
		wantError      string
		wantMarkerText string
	}{
		{
			name:           "success",
			outcome:        "success",
			wantExitCode:   0,
			wantStatusJSON: true,
			wantComplete:   true,
			wantMarkerText: "FAKE_AGENT: running with outcome=success",
		},
		{
			name:           "slow_success",
			outcome:        "slow_success",
			wantExitCode:   0,
			wantStatusJSON: true,
			wantComplete:   true,
			wantMarkerText: "FAKE_AGENT: running with outcome=slow_success",
		},
		{
			name:           "fail",
			outcome:        "fail",
			wantExitCode:   1,
			wantStatusJSON: true,
			wantComplete:   false,
			wantError:      "fake agent failure",
			wantMarkerText: "FAKE_AGENT: running with outcome=fail",
		},
		{
			name:           "needs_input",
			outcome:        "needs_input",
			wantExitCode:   1,
			wantStatusJSON: true,
			wantComplete:   false,
			wantNeedsInput: true,
			wantQuestion:   "fake question",
			wantMarkerText: "FAKE_AGENT: running with outcome=needs_input",
		},
		{
			name:         "crash",
			outcome:      "crash",
			wantExitCode: 137,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containerID := runFakeAgent(t, tt.outcome)
			defer removeContainer(t, containerID)

			exitCode := waitContainer(t, containerID, 10*time.Second)
			if exitCode != tt.wantExitCode {
				t.Errorf("exit code = %d, want %d", exitCode, tt.wantExitCode)
			}

			logs := containerLogs(t, containerID)

			// Check marker text
			if tt.wantMarkerText != "" {
				if !strings.Contains(logs, tt.wantMarkerText) {
					t.Errorf("stdout missing marker text %q\ngot: %s", tt.wantMarkerText, logs)
				}
			}

			// Check status.json
			statusData := copyStatusJSON(t, containerID)
			if tt.wantStatusJSON {
				if statusData == nil {
					t.Fatal("expected status.json to exist, but it was not found")
				}

				// Verify it parses into AgentStatus (contract compatibility)
				var agentStatus orchestrator.AgentStatus
				if err := json.Unmarshal(statusData, &agentStatus); err != nil {
					t.Fatalf("status.json failed to parse into AgentStatus: %v\nraw: %s", err, statusData)
				}

				if agentStatus.Complete != tt.wantComplete {
					t.Errorf("status.json complete = %v, want %v", agentStatus.Complete, tt.wantComplete)
				}
				if agentStatus.NeedsInput != tt.wantNeedsInput {
					t.Errorf("status.json needs_input = %v, want %v", agentStatus.NeedsInput, tt.wantNeedsInput)
				}
				if agentStatus.Question != tt.wantQuestion {
					t.Errorf("status.json question = %q, want %q", agentStatus.Question, tt.wantQuestion)
				}
				if agentStatus.Error != tt.wantError {
					t.Errorf("status.json error = %q, want %q", agentStatus.Error, tt.wantError)
				}

				// Check BACKFLOW_STATUS_JSON line matches the file
				logPayload := parseBackliteStatusLine(t, logs)
				if logPayload == nil {
					t.Fatal("expected BACKFLOW_STATUS_JSON: line in stdout, but not found")
				}
				var logStatus orchestrator.AgentStatus
				if err := json.Unmarshal(logPayload, &logStatus); err != nil {
					t.Fatalf("BACKFLOW_STATUS_JSON line failed to parse: %v\nraw: %s", err, logPayload)
				}
				if !reflect.DeepEqual(logStatus, agentStatus) {
					t.Errorf("BACKFLOW_STATUS_JSON status != status.json\nlog:  %+v\nfile: %+v", logStatus, agentStatus)
				}
			} else {
				if statusData != nil {
					t.Errorf("expected no status.json for outcome %q, but found one: %s", tt.outcome, statusData)
				}
				logPayload := parseBackliteStatusLine(t, logs)
				if logPayload != nil {
					t.Errorf("expected no BACKFLOW_STATUS_JSON line for outcome %q, but found: %s", tt.outcome, logPayload)
				}
			}
		})
	}
}

func TestFakeAgentTimeout(t *testing.T) {
	containerID := runFakeAgent(t, "timeout")
	defer removeContainer(t, containerID)

	// The timeout outcome should sleep indefinitely. Give it 2 seconds then
	// stop it — it should not have exited on its own.
	time.Sleep(2 * time.Second)

	// Stop the container (sends SIGTERM, then SIGKILL after grace period)
	if err := exec.Command("docker", "stop", "-t", "2", containerID).Run(); err != nil {
		t.Fatalf("docker stop failed: %v", err)
	}

	exitCode := waitContainer(t, containerID, 10*time.Second)
	// Exit code should be non-zero (137 from SIGKILL or 143 from SIGTERM)
	if exitCode == 0 {
		t.Error("timeout outcome should not exit 0")
	}

	// No status.json should exist
	statusData := copyStatusJSON(t, containerID)
	if statusData != nil {
		t.Errorf("expected no status.json for timeout outcome, but found: %s", statusData)
	}

	// No BACKFLOW_STATUS_JSON line
	logs := containerLogs(t, containerID)
	logPayload := parseBackliteStatusLine(t, logs)
	if logPayload != nil {
		t.Errorf("expected no BACKFLOW_STATUS_JSON line for timeout, but found: %s", logPayload)
	}
}
