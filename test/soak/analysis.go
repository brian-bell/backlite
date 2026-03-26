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
	TasksCancelled   int
}

// ScenarioOutcome tracks attempt/pass/fail counts for a multi-step scenario type.
type ScenarioOutcome struct {
	Attempted int
	Passed    int
	Failed    int
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
// scenarios maps multi-step scenario names to their outcomes (may be nil).
func Analyze(samples []MetricSample, tasksSubmitted int, scenarios map[string]ScenarioOutcome) Report {
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

	// Error rate: fail if > 50% of completed+failed tasks are failures.
	// Threshold is 50% (not 10%) because the scenario mix intentionally
	// includes fail, needs_input, and retry outcomes.
	totalFinished := final.TasksCompleted + final.TasksFailed
	if totalFinished > 0 {
		errorRate := float64(final.TasksFailed) / float64(totalFinished)
		if errorRate > 0.50 {
			failures = append(failures, Failure{
				Name:    "error_rate",
				Message: fmt.Sprintf("error rate %.1f%% (%d/%d) exceeds 50%% threshold", errorRate*100, final.TasksFailed, totalFinished),
			})
		}
	}

	// Scenario health: for multi-step scenarios with enough attempts,
	// fail if more than half of the attempts failed.
	for name, sc := range scenarios {
		if sc.Attempted >= 3 && sc.Failed > sc.Attempted/2 {
			failures = append(failures, Failure{
				Name:    "scenario_health",
				Message: fmt.Sprintf("scenario %q: %d/%d attempts failed", name, sc.Failed, sc.Attempted),
			})
		}
	}

	return Report{
		Pass:     len(failures) == 0,
		Failures: failures,
	}
}
