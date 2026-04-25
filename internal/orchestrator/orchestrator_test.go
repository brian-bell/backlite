package orchestrator

import (
	"testing"
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
