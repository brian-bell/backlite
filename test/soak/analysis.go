package main

import "fmt"

// MetricSample holds a single point-in-time observation collected during the soak test.
type MetricSample struct {
	RSSKB            int64 // resident set size in KB
	PoolAcquired     int32
	PoolMax          int32
	ExitedContainers int
	TasksCompleted   int
	TasksFailed      int
}

// Failure describes a single threshold violation.
type Failure struct {
	Name    string // e.g. "rss_growth", "pool_exhaustion"
	Message string
}

// Report summarizes the soak test analysis.
type Report struct {
	Pass     bool
	Failures []Failure
}

// Analyze checks collected metric samples against thresholds and returns a report.
// tasksSubmitted is the total number of tasks submitted during the test.
func Analyze(samples []MetricSample, tasksSubmitted int) Report {
	var failures []Failure

	if len(samples) < 2 {
		return Report{Pass: true}
	}

	baseline := samples[0]
	final := samples[len(samples)-1]

	// RSS growth: fail if final > 2x baseline
	if baseline.RSSKB > 0 && final.RSSKB > 2*baseline.RSSKB {
		failures = append(failures, Failure{
			Name:    "rss_growth",
			Message: fmt.Sprintf("RSS grew from %dKB to %dKB (%.1fx)", baseline.RSSKB, final.RSSKB, float64(final.RSSKB)/float64(baseline.RSSKB)),
		})
	}

	// Pool exhaustion: fail if acquired ever exceeded max
	for i, s := range samples {
		if s.PoolMax > 0 && s.PoolAcquired > s.PoolMax {
			failures = append(failures, Failure{
				Name:    "pool_exhaustion",
				Message: fmt.Sprintf("sample %d: acquired conns (%d) exceeded max (%d)", i, s.PoolAcquired, s.PoolMax),
			})
			break
		}
	}

	// Container accumulation: fail if exited containers > 2x tasks submitted
	if tasksSubmitted > 0 && final.ExitedContainers > 2*tasksSubmitted {
		failures = append(failures, Failure{
			Name:    "container_accumulation",
			Message: fmt.Sprintf("exited containers (%d) exceeded 2x tasks submitted (%d)", final.ExitedContainers, tasksSubmitted),
		})
	}

	// Error rate: fail if > 10% of completed+failed tasks are failures
	totalFinished := final.TasksCompleted + final.TasksFailed
	if totalFinished > 0 {
		errorRate := float64(final.TasksFailed) / float64(totalFinished)
		if errorRate > 0.10 {
			failures = append(failures, Failure{
				Name:    "error_rate",
				Message: fmt.Sprintf("error rate %.1f%% (%d/%d) exceeds 10%% threshold", errorRate*100, final.TasksFailed, totalFinished),
			})
		}
	}

	return Report{
		Pass:     len(failures) == 0,
		Failures: failures,
	}
}
