package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	var (
		duration     = flag.Duration("duration", 1*time.Hour, "total test duration")
		short        = flag.Bool("short", false, "run a short soak test (10 minutes)")
		taskInterval = flag.Duration("task-interval", 30*time.Second, "interval between task submissions")
		apiURL       = flag.String("api-url", "http://localhost:8080", "Backflow API base URL")
		agentImage   = flag.String("agent-image", "backflow-fake-agent:test", "agent image name for container counting")
		databaseURL  = flag.String("database-url", os.Getenv("BACKFLOW_DATABASE_URL"), "PostgreSQL connection string (default: $BACKFLOW_DATABASE_URL)")
	)
	flag.Parse()

	if *short {
		*duration = 10 * time.Minute
	}

	fmt.Printf("==> Soak test starting: duration=%s task-interval=%s api-url=%s\n", *duration, *taskInterval, *apiURL)

	// Prune stale containers from previous runs so the baseline starts at 0.
	pruneStaleContainers(*agentImage)

	// Truncate the tasks table so counts from previous runs don't pollute metrics.
	if *databaseURL != "" {
		truncateTasks(*databaseURL)
	} else {
		fmt.Println("  [warn] no --database-url or BACKFLOW_DATABASE_URL; skipping task table truncation")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(*duration)

	var (
		mu             sync.Mutex
		samples        []MetricSample
		tasksSubmitted int
		submitErrors   int
	)

	// Discover PID from /debug/stats on first collection.
	var serverPID int

	// --- Task submission loop ---
	go func() {
		submitTicker := time.NewTicker(*taskInterval)
		defer submitTicker.Stop()

		for time.Now().Before(deadline) {
			<-submitTicker.C
			if time.Now().After(deadline) {
				return
			}

			err := submitTask(client, *apiURL)
			mu.Lock()
			tasksSubmitted++
			if err != nil {
				submitErrors++
				fmt.Printf("  [warn] task submit error: %v\n", err)
			}
			submitted := tasksSubmitted
			mu.Unlock()
			fmt.Printf("  [submit] task #%d submitted\n", submitted)
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

		fmt.Printf("  [metric] sample #%d: rss=%dKB pool=%d/%d completed=%d failed=%d containers=%d\n",
			sampleCount, s.RSSKB, s.PoolAcquired, s.PoolMax, s.TasksCompleted, s.TasksFailed, s.ExitedContainers)
	}

	// --- Final analysis ---
	fmt.Println("\n==> Soak test complete. Analyzing results...")

	mu.Lock()
	finalSamples := make([]MetricSample, len(samples))
	copy(finalSamples, samples)
	finalSubmitted := tasksSubmitted
	mu.Unlock()

	report := Analyze(finalSamples, finalSubmitted)

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
		fmt.Printf("Exited containers: %d\n", last.ExitedContainers)
	}

	// --- Post-test cleanup ---
	fmt.Println("\n==> Cleaning up...")
	pruneStaleContainers(*agentImage)
	if *databaseURL != "" {
		truncateTasks(*databaseURL)
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

// submitTask POSTs a new task with FAKE_OUTCOME=success.
func submitTask(client *http.Client, apiURL string) error {
	body := `{"prompt":"soak test task","save_agent_output":false,"env_vars":{"FAKE_OUTCOME":"success"}}`
	resp, err := client.Post(apiURL+"/api/v1/tasks", "application/json", bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
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

	// 4. Count completed and failed tasks
	sample.TasksCompleted = countTasksByStatus(client, apiURL, "completed")
	sample.TasksFailed = countTasksByStatus(client, apiURL, "failed")

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

// truncateTasks connects to PostgreSQL and truncates the tasks table.
func truncateTasks(databaseURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		fmt.Printf("  [warn] failed to connect to database: %v\n", err)
		return
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, "TRUNCATE tasks CASCADE"); err != nil {
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
