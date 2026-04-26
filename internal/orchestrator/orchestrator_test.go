package orchestrator

import (
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/config"
)

func TestRunning_ReflectsIncrementAndDecrement(t *testing.T) {
	ms := newMockStore()
	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(ms, bus)

	if got := o.Running(); got != 0 {
		t.Fatalf("Running() = %d, want 0", got)
	}

	o.incrementRunning()
	o.incrementRunning()
	if got := o.Running(); got != 2 {
		t.Fatalf("Running() = %d, want 2", got)
	}

	o.decrementRunning()
	if got := o.Running(); got != 1 {
		t.Fatalf("Running() = %d, want 1", got)
	}
}

func TestTick_SchedulesLocalBackups(t *testing.T) {
	ms := newMockStore()
	bus, _ := newTestBus()
	defer bus.Close()

	scheduler := &mockBackupScheduler{}
	o := newTestOrchestrator(ms, bus, withBackups(scheduler))

	o.tick(t.Context())

	if got := scheduler.Calls(); got != 1 {
		t.Fatalf("MaybeSchedule() calls = %d, want 1", got)
	}
}

// TestNew_PropagatesLocalBackupRetention pins that the orchestrator's
// constructor wires cfg.LocalBackupRetention into the backup manager.
// Without this test, the manager defaults to Retention=0 in production
// (pruning silently disabled) even though the env var is loaded and
// validated upstream — see PR #79 review.
func TestNew_PropagatesLocalBackupRetention(t *testing.T) {
	ms := newMockStore()
	bus, _ := newTestBus()
	defer bus.Close()

	const wantRetention = 7 * 24 * time.Hour
	cfg := &config.Config{
		MaxContainers:        4,
		MaxUserRetries:       2,
		PollInterval:         5 * time.Second,
		LocalBackupEnabled:   true,
		LocalBackupDir:       t.TempDir(),
		LocalBackupInterval:  time.Hour,
		LocalBackupRetention: wantRetention,
	}

	o := New(ms, cfg, bus, &mockDockerManager{}, nil, nil)

	if got := o.BackupStatus().Retention; got != wantRetention {
		t.Fatalf("BackupStatus().Retention = %v, want %v", got, wantRetention)
	}
}
