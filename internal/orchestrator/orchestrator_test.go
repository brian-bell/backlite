package orchestrator

import (
	"fmt"
	"testing"

	"github.com/backflow-labs/backflow/internal/models"
)

func TestInitLocalMode_DBError_DoesNotCreateInstance(t *testing.T) {
	ms := newMockStore()
	ms.getInstanceErr = fmt.Errorf("disk I/O error")

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(ms, bus)
	o.initLocalMode()

	// On a real DB error, initLocalMode should bail out — not create an instance.
	if _, exists := ms.instances["local"]; exists {
		t.Fatal("expected no instance to be created when GetInstance returns a real DB error")
	}
}

func TestInitEC2Mode_DBError_DoesNotTerminateLocalInstance(t *testing.T) {
	ms := newMockStore()
	// Seed a running local instance — simulating a leftover from local-mode.
	ms.instances["local"] = &models.Instance{
		InstanceID: "local",
		Status:     models.InstanceStatusRunning,
	}
	// Inject a DB error so GetInstance fails.
	ms.getInstanceErr = fmt.Errorf("disk I/O error")

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(ms, bus)
	o.initEC2Mode()

	// Should not have terminated the local instance — we couldn't confirm it exists.
	if ms.instances["local"].Status == models.InstanceStatusTerminated {
		t.Fatal("expected local instance NOT to be terminated when GetInstance returns a real DB error")
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

func TestInitFargateMode_DBError_DoesNotCreateInstance(t *testing.T) {
	ms := newMockStore()
	ms.getInstanceErr = fmt.Errorf("disk I/O error")
	ms.instances["stale"] = &models.Instance{
		InstanceID: "stale",
		Status:     models.InstanceStatusRunning,
	}

	bus, _ := newTestBus()
	defer bus.Close()
	o := newTestOrchestrator(ms, bus)
	o.config.MaxConcurrentTasks = 5
	o.initFargateMode()

	// On a real DB error, initFargateMode should bail out — not create an instance.
	if _, exists := ms.instances["fargate"]; exists {
		t.Fatal("expected no instance to be created when GetInstance returns a real DB error")
	}
	if ms.instances["stale"].Status == models.InstanceStatusTerminated {
		t.Fatal("expected stale instances to remain untouched when GetInstance returns a real DB error")
	}
}
