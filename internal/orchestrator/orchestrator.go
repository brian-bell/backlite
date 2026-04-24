package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/embeddings"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/store"
)

// maxInspectFailures is the number of consecutive container inspect failures
// before a task is killed or requeued.
const maxInspectFailures = 3

// Orchestrator manages the lifecycle of tasks: dispatching them, monitoring
// their containers, handling completions, and recovering from restarts.
type Orchestrator struct {
	store    store.Store
	config   *config.Config
	bus      *notify.EventBus
	docker   Runner
	outputs  Writer
	embedder embeddings.Embedder

	mu              sync.Mutex
	running         int
	stopCh          chan struct{}
	inspectFailures map[string]int // task ID -> consecutive inspect failure count
}

func New(s store.Store, cfg *config.Config, bus *notify.EventBus, runner Runner, outputs Writer, embedder embeddings.Embedder) *Orchestrator {
	return &Orchestrator{
		store:           s,
		config:          cfg,
		bus:             bus,
		docker:          runner,
		outputs:         outputs,
		embedder:        embedder,
		stopCh:          make(chan struct{}),
		inspectFailures: make(map[string]int),
	}
}

// Running returns the current count of running tasks.
func (o *Orchestrator) Running() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.running
}

// Docker returns the Runner for use by the API logs endpoint.
func (o *Orchestrator) Docker() Runner {
	return o.docker
}

// Start begins the orchestrator poll loop, recovering orphaned tasks first.
func (o *Orchestrator) Start(ctx context.Context) {
	log.Info().
		Str("agent_image", o.config.AgentImage).
		Int("max_concurrent", o.config.MaxConcurrent()).
		Dur("poll_interval", o.config.PollInterval).
		Msg("orchestrator started")

	o.recoverOnStartup(ctx)

	ticker := time.NewTicker(o.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("orchestrator stopping")
			return
		case <-o.stopCh:
			log.Info().Msg("orchestrator stopped")
			return
		case <-ticker.C:
			o.tick(ctx)
		}
	}
}

// Stop signals the orchestrator to exit its poll loop.
func (o *Orchestrator) Stop() {
	close(o.stopCh)
}

// tick runs a single orchestration cycle: monitor, dispatch.
func (o *Orchestrator) tick(ctx context.Context) {
	o.monitorCancelled(ctx)
	o.monitorRecovering(ctx)
	o.monitorRunning(ctx)
	o.dispatchPending(ctx)
}

// --- Shared helpers used across dispatch, monitor, and recovery ---

// incrementRunning safely increments the running task counter.
func (o *Orchestrator) incrementRunning() {
	o.mu.Lock()
	o.running++
	o.mu.Unlock()
}

// decrementRunning safely decrements the running task counter.
func (o *Orchestrator) decrementRunning() {
	o.mu.Lock()
	if o.running > 0 {
		o.running--
	}
	o.mu.Unlock()
}

// releaseSlot decrements the in-memory running task counter. Capacity is
// persisted as the live count of provisioning/running tasks in the `tasks`
// table, so there is nothing else to release.
func (o *Orchestrator) releaseSlot(_ context.Context, _ *models.Task) {
	o.decrementRunning()
}
