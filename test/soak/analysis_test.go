package main

import (
	"testing"
)

func TestAnalyze_StableMetrics_Passes(t *testing.T) {
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 2, PoolMax: 10, ExitedContainers: 0, TasksCompleted: 0, TasksFailed: 0},
		{RSSKB: 51000, PoolAcquired: 3, PoolMax: 10, ExitedContainers: 1, TasksCompleted: 2, TasksFailed: 0},
		{RSSKB: 52000, PoolAcquired: 2, PoolMax: 10, ExitedContainers: 2, TasksCompleted: 4, TasksFailed: 0},
		{RSSKB: 50500, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 3, TasksCompleted: 6, TasksFailed: 0},
	}

	report := Analyze(samples, 10, nil)

	if !report.Pass {
		t.Errorf("expected pass, got fail: %v", report.Failures)
	}
}

func TestAnalyze_RSSGrowth_Fails(t *testing.T) {
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 0, TasksCompleted: 0, TasksFailed: 0},
		{RSSKB: 60000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 1, TasksCompleted: 2, TasksFailed: 0},
		{RSSKB: 80000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 2, TasksCompleted: 4, TasksFailed: 0},
		{RSSKB: 110000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 3, TasksCompleted: 6, TasksFailed: 0},
	}

	report := Analyze(samples, 10, nil)

	if report.Pass {
		t.Error("expected fail due to RSS growth > 2x")
	}
	if !containsFailure(report.Failures, "rss_growth") {
		t.Errorf("expected rss_growth failure, got: %v", report.Failures)
	}
}

func TestAnalyze_PoolExhaustion_Fails(t *testing.T) {
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 2, PoolMax: 10, ExitedContainers: 0, TasksCompleted: 0, TasksFailed: 0},
		{RSSKB: 50000, PoolAcquired: 11, PoolMax: 10, ExitedContainers: 1, TasksCompleted: 2, TasksFailed: 0},
	}

	report := Analyze(samples, 5, nil)

	if report.Pass {
		t.Error("expected fail due to pool exhaustion")
	}
	if !containsFailure(report.Failures, "pool_exhaustion") {
		t.Errorf("expected pool_exhaustion failure, got: %v", report.Failures)
	}
}

func TestAnalyze_ContainerAccumulation_Fails(t *testing.T) {
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 0, TasksCompleted: 0, TasksFailed: 0},
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 25, TasksCompleted: 5, TasksFailed: 0},
	}

	report := Analyze(samples, 5, nil)

	if report.Pass {
		t.Error("expected fail due to container accumulation")
	}
	if !containsFailure(report.Failures, "container_accumulation") {
		t.Errorf("expected container_accumulation failure, got: %v", report.Failures)
	}
}

func TestAnalyze_HighErrorRate_Fails(t *testing.T) {
	// Error rate threshold is 50% (intentional failures in the mix are expected).
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 0, TasksCompleted: 0, TasksFailed: 0},
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 5, TasksCompleted: 3, TasksFailed: 7},
	}

	report := Analyze(samples, 10, nil)

	if report.Pass {
		t.Error("expected fail due to high error rate (>50%)")
	}
	if !containsFailure(report.Failures, "error_rate") {
		t.Errorf("expected error_rate failure, got: %v", report.Failures)
	}
}

func TestAnalyze_ModerateErrorRate_Passes(t *testing.T) {
	// 40% failure rate is below the 50% threshold (expected with intentional failures).
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 0, TasksCompleted: 0, TasksFailed: 0},
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, ExitedContainers: 5, TasksCompleted: 6, TasksFailed: 4},
	}

	report := Analyze(samples, 10, nil)

	if !report.Pass {
		t.Errorf("expected pass (40%% error rate is below 50%% threshold), got: %v", report.Failures)
	}
}

func TestAnalyze_TooFewSamples_Passes(t *testing.T) {
	// With fewer than 2 samples, analysis can't detect trends — should pass vacuously.
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10},
	}

	report := Analyze(samples, 0, nil)

	if !report.Pass {
		t.Errorf("expected pass with < 2 samples, got: %v", report.Failures)
	}
}

func TestAnalyze_ScenarioHealth_Passes(t *testing.T) {
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10},
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, TasksCompleted: 5},
	}
	scenarios := map[string]ScenarioOutcome{
		"cancel":      {Attempted: 10, Passed: 8, Failed: 2},
		"retry_cycle": {Attempted: 6, Passed: 5, Failed: 1},
		"retry_limit": {Attempted: 4, Passed: 3, Failed: 1},
	}

	report := Analyze(samples, 20, scenarios)

	if !report.Pass {
		t.Errorf("expected pass (all scenarios healthy), got: %v", report.Failures)
	}
}

func TestAnalyze_ScenarioHealth_Fails(t *testing.T) {
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10},
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, TasksCompleted: 5},
	}
	scenarios := map[string]ScenarioOutcome{
		"cancel":      {Attempted: 10, Passed: 3, Failed: 7}, // 70% failure
		"retry_cycle": {Attempted: 6, Passed: 5, Failed: 1},
	}

	report := Analyze(samples, 20, scenarios)

	if report.Pass {
		t.Error("expected fail due to cancel scenario health")
	}
	if !containsFailure(report.Failures, "scenario_health") {
		t.Errorf("expected scenario_health failure, got: %v", report.Failures)
	}
}

func TestAnalyze_ScenarioHealth_SkipsLowAttempts(t *testing.T) {
	samples := []MetricSample{
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10},
		{RSSKB: 50000, PoolAcquired: 1, PoolMax: 10, TasksCompleted: 5},
	}
	// Only 2 attempts — below the 3-attempt threshold for health check
	scenarios := map[string]ScenarioOutcome{
		"cancel": {Attempted: 2, Passed: 0, Failed: 2},
	}

	report := Analyze(samples, 5, scenarios)

	if !report.Pass {
		t.Errorf("expected pass (too few attempts to judge), got: %v", report.Failures)
	}
}

func containsFailure(failures []Failure, name string) bool {
	for _, f := range failures {
		if f.Name == name {
			return true
		}
	}
	return false
}
