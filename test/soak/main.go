package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	var (
		duration     = flag.Duration("duration", 1*time.Hour, "total test duration")
		short        = flag.Bool("short", false, "run a short soak test (10 minutes)")
		taskInterval = flag.Duration("task-interval", 3*time.Second, "interval between task submissions")
		apiURL       = flag.String("api-url", "http://localhost:8080", "Backflow API base URL")
		agentImage   = flag.String("agent-image", "backflow-fake-agent:test", "agent image name for container counting")
		databasePath = flag.String("database-path", os.Getenv("BACKFLOW_DATABASE_PATH"), "SQLite database path (default: $BACKFLOW_DATABASE_PATH)")
		maxRetries   = flag.Int("max-retries", 2, "max user retries (must match server BACKFLOW_MAX_USER_RETRIES)")
	)
	flag.Parse()

	if *short {
		*duration = 10 * time.Minute
	}

	fmt.Printf("==> Soak test starting: duration=%s task-interval=%s api-url=%s\n", *duration, *taskInterval, *apiURL)

	// Prune stale containers from previous runs so the baseline starts at 0.
	pruneStaleContainers(*agentImage)

	// Truncate the tasks table so counts from previous runs don't pollute metrics.
	if *databasePath != "" {
		truncateTasks(*databasePath)
	} else {
		fmt.Println("  [warn] no --database-path or BACKFLOW_DATABASE_PATH; skipping task table truncation")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(*duration)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var (
		mu             sync.Mutex
		samples        []MetricSample
		tasksSubmitted int
		submitErrors   int
	)

	stats := newScenarioStats()

	// Discover PID from /debug/stats on first collection.
	var serverPID int

	// --- Task submission loop ---
	var wg sync.WaitGroup
	multiStepSem := make(chan struct{}, 3) // limit concurrent multi-step scenarios (server needs BACKFLOW_CONTAINERS_PER_INSTANCE >= 4)

	go func() {
		submitTicker := time.NewTicker(*taskInterval)
		defer submitTicker.Stop()

		for time.Now().Before(deadline) {
			<-submitTicker.C
			if time.Now().After(deadline) {
				return
			}

			sc := pickScenario()

			if sc.MultiStep {
				// Try to acquire the semaphore; downgrade to success if full.
				select {
				case multiStepSem <- struct{}{}:
					wg.Add(1)
					go func(s scenario) {
						defer func() { <-multiStepSem; wg.Done() }()
						switch s.Name {
						case "cancel":
							runCancelScenario(ctx, client, *apiURL, stats)
						case "retry_cycle":
							runRetryCycleScenario(ctx, client, *apiURL, stats)
						case "retry_limit":
							runRetryLimitScenario(ctx, client, *apiURL, *maxRetries, stats)
						}
					}(sc)
					mu.Lock()
					tasksSubmitted++
					submitted := tasksSubmitted
					mu.Unlock()
					fmt.Printf("  [submit] task #%d: %s (multi-step)\n", submitted, sc.Name)
					continue
				default:
					sc = scenarioTable[0] // semaphore full, downgrade
				}
			}

			// Fire-and-forget: create the task and move on.
			_, err := createTask(client, *apiURL, sc.FakeOutcome)
			mu.Lock()
			tasksSubmitted++
			if err != nil {
				submitErrors++
				fmt.Printf("  [warn] task submit error (%s): %v\n", sc.Name, err)
			}
			submitted := tasksSubmitted
			mu.Unlock()
			fmt.Printf("  [submit] task #%d: %s\n", submitted, sc.Name)
		}
	}()

	// --- Metric collection loop ---
	collectionInterval := 60 * time.Second
	collectionTicker := time.NewTicker(collectionInterval)
	defer collectionTicker.Stop()

	// Collect initial baseline after a brief warmup.
	time.Sleep(5 * time.Second)
	if s, pid, err := collectMetrics(client, *apiURL, *agentImage, serverPID); err == nil {
		mu.Lock()
		samples = append(samples, s)
		mu.Unlock()
		serverPID = pid
		fmt.Printf("  [metric] baseline: rss=%dKB pool=%d/%d containers=%d\n",
			s.RSSKB, s.PoolAcquired, s.PoolMax, s.ExitedContainers)
	} else {
		fmt.Printf("  [warn] baseline collection failed: %v\n", err)
	}

	for time.Now().Before(deadline) {
		<-collectionTicker.C
		if time.Now().After(deadline) {
			break
		}

		s, pid, err := collectMetrics(client, *apiURL, *agentImage, serverPID)
		if err != nil {
			fmt.Printf("  [warn] metric collection error: %v\n", err)
			continue
		}
		serverPID = pid

		mu.Lock()
		samples = append(samples, s)
		sampleCount := len(samples)
		mu.Unlock()

		fmt.Printf("  [metric] sample #%d: rss=%dKB pool=%d/%d completed=%d failed=%d cancelled=%d containers=%d\n",
			sampleCount, s.RSSKB, s.PoolAcquired, s.PoolMax, s.TasksCompleted, s.TasksFailed, s.TasksCancelled, s.ExitedContainers)
	}

	// Wait for in-flight multi-step scenarios to finish (with a grace period).
	fmt.Println("\n==> Waiting for in-flight scenarios...")
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		fmt.Println("  [warn] timed out waiting for in-flight scenarios")
	}

	// --- Final analysis ---
	fmt.Println("\n==> Soak test complete. Analyzing results...")

	mu.Lock()
	finalSamples := make([]MetricSample, len(samples))
	copy(finalSamples, samples)
	finalSubmitted := tasksSubmitted
	mu.Unlock()

	scenarioSnap := stats.snapshot()
	report := Analyze(finalSamples, finalSubmitted, scenarioSnap)

	fmt.Printf("\n--- Soak Test Report ---\n")
	fmt.Printf("Duration:        %s\n", *duration)
	fmt.Printf("Tasks submitted: %d\n", finalSubmitted)
	fmt.Printf("Samples:         %d\n", len(finalSamples))
	fmt.Printf("Submit errors:   %d\n", submitErrors)

	if len(finalSamples) > 0 {
		first := finalSamples[0]
		last := finalSamples[len(finalSamples)-1]
		fmt.Printf("RSS baseline:    %dKB\n", first.RSSKB)
		fmt.Printf("RSS final:       %dKB\n", last.RSSKB)
		fmt.Printf("Completed:       %d\n", last.TasksCompleted)
		fmt.Printf("Failed:          %d\n", last.TasksFailed)
		fmt.Printf("Cancelled:       %d\n", last.TasksCancelled)
		fmt.Printf("Exited containers: %d\n", last.ExitedContainers)
	}

	if len(scenarioSnap) > 0 {
		fmt.Printf("\n--- Scenario Stats ---\n")
		for name, sc := range scenarioSnap {
			fmt.Printf("  %-14s attempted=%d passed=%d failed=%d\n", name, sc.Attempted, sc.Passed, sc.Failed)
		}
	}

	// --- Post-test cleanup ---
	fmt.Println("\n==> Cleaning up...")
	pruneStaleContainers(*agentImage)
	if *databasePath != "" {
		truncateTasks(*databasePath)
	}

	if report.Pass {
		fmt.Println("\nResult: PASS")
	} else {
		fmt.Println("\nResult: FAIL")
		for _, f := range report.Failures {
			fmt.Printf("  - [%s] %s\n", f.Name, f.Message)
		}
		os.Exit(1)
	}
}

// debugStatsResponse mirrors the /debug/stats JSON shape.
type debugStatsResponse struct {
	Data struct {
		Orchestrator struct {
			RunningTasks int `json:"running_tasks"`
		} `json:"orchestrator"`
		Pool struct {
			AcquiredConns int32 `json:"acquired_conns"`
			IdleConns     int32 `json:"idle_conns"`
			TotalConns    int32 `json:"total_conns"`
			MaxConns      int32 `json:"max_conns"`
		} `json:"pool"`
		UptimeSeconds float64 `json:"uptime_seconds"`
		PID           int     `json:"pid"`
	} `json:"data"`
}

// collectMetrics gathers one metric sample from the running Backflow instance.
func collectMetrics(client *http.Client, apiURL, agentImage string, knownPID int) (MetricSample, int, error) {
	var sample MetricSample

	// 1. Fetch /debug/stats
	resp, err := client.Get(apiURL + "/debug/stats")
	if err != nil {
		return sample, knownPID, fmt.Errorf("GET /debug/stats: %w", err)
	}
	defer resp.Body.Close()

	var stats debugStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return sample, knownPID, fmt.Errorf("decode /debug/stats: %w", err)
	}

	sample.PoolAcquired = stats.Data.Pool.AcquiredConns
	sample.PoolMax = stats.Data.Pool.MaxConns
	pid := stats.Data.PID

	// 2. Measure RSS via ps
	if pid > 0 {
		sample.RSSKB = measureRSS(pid)
	}

	// 3. Count exited containers
	sample.ExitedContainers = countContainers(agentImage)

	// 4. Count tasks by terminal status
	sample.TasksCompleted = countTasksByStatus(client, apiURL, "completed")
	sample.TasksFailed = countTasksByStatus(client, apiURL, "failed")
	sample.TasksCancelled = countTasksByStatus(client, apiURL, "cancelled")

	return sample, pid, nil
}

// measureRSS returns the RSS in KB for the given PID, or 0 on error.
func measureRSS(pid int) int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	val, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return val
}

// truncateTasks connects to SQLite and truncates task-related tables.
func truncateTasks(databasePath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		fmt.Printf("  [warn] failed to connect to database: %v\n", err)
		return
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
		DELETE FROM readings;
		DELETE FROM discord_task_threads;
		DELETE FROM discord_installs;
		DELETE FROM allowed_senders;
		DELETE FROM api_keys;
		DELETE FROM instances;
		DELETE FROM tasks;`); err != nil {
		fmt.Printf("  [warn] failed to truncate tasks: %v\n", err)
		return
	}
	fmt.Println("  [cleanup] truncated tasks table")
}

// pruneStaleContainers removes stopped containers from previous runs so the
// baseline container count starts at zero.
func pruneStaleContainers(agentImage string) {
	out, err := exec.Command("docker", "ps", "-a", "-q",
		"--filter", "ancestor="+agentImage,
		"--filter", "status=exited",
	).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return
	}
	ids := strings.Fields(strings.TrimSpace(string(out)))
	fmt.Printf("  [cleanup] removing %d stale containers\n", len(ids))
	args := append([]string{"rm"}, ids...)
	exec.Command("docker", args...).Run()
}

// countContainers counts all containers (running and exited) for the given image.
func countContainers(agentImage string) int {
	out, err := exec.Command("docker", "ps", "-a",
		"--filter", "ancestor="+agentImage,
		"--format", "{{.ID}}",
	).Output()
	if err != nil {
		return 0
	}
	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return 0
	}
	return len(strings.Split(lines, "\n"))
}

// countTasksByStatus queries the API for the count of tasks with the given status.
func countTasksByStatus(client *http.Client, apiURL, status string) int {
	resp, err := client.Get(fmt.Sprintf("%s/api/v1/tasks?status=%s&limit=1000", apiURL, status))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&envelope)
	return len(envelope.Data)
}
