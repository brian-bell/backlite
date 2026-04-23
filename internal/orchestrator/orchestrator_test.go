package orchestrator

import (
	"fmt"
	"testing"
)

func TestInitInstance_DBError_DoesNotCreateInstance(t *testing.T) {
	ms := newMockStore()
	ms.getInstanceErr = fmt.Errorf("disk I/O error")

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(ms, bus)
	o.initInstance()

	// On a real DB error, initInstance should bail out — not create an instance.
	if _, exists := ms.instances["local"]; exists {
		t.Fatal("expected no instance to be created when GetInstance returns a real DB error")
	}
}

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
