package orchestrator

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/config"
	"github.com/brian-bell/backlite/internal/embeddings"
	"github.com/brian-bell/backlite/internal/models"
	"github.com/brian-bell/backlite/internal/notify"
	"github.com/brian-bell/backlite/internal/orchestrator/lifecycle"
	"github.com/brian-bell/backlite/internal/store"
)

// maxInspectFailures is the number of consecutive container inspect failures
// before a task is killed or requeued.
const maxInspectFailures = 3

// inspectOutcome is the decision returned by classifyInspectFailure. It is the
// shared structural skeleton for monitor.go's handleInspectError and
// recovery.go's handleRecoveringInspectError — each caller picks its own
// action per outcome (kill vs. requeue) while the counter bookkeeping and
// instance-gone detection live in one place.
type inspectOutcome int

const (
	inspectContinue          inspectOutcome = iota // under threshold; keep polling
	inspectInstanceGone                            // IsInstanceGone(err) was true
	inspectExceededThreshold                       // ≥ maxInspectFailures consecutive failures
)

func (o *Orchestrator) classifyInspectFailure(taskID string, err error) (inspectOutcome, int) {
	if IsInstanceGone(err) {
		delete(o.inspectFailures, taskID)
		return inspectInstanceGone, 0
	}
	o.inspectFailures[taskID]++
	count := o.inspectFailures[taskID]
	if count >= maxInspectFailures {
		delete(o.inspectFailures, taskID)
		return inspectExceededThreshold, count
	}
	return inspectContinue, count
}

// localInstanceID is the synthetic instance row used to track local Docker
// capacity. There is exactly one instance now that the service only runs
// containers on the local host.
const localInstanceID = "local"

// Orchestrator manages the lifecycle of tasks: dispatching them to instances,
// monitoring their containers, handling completions, and recovering from restarts.
type Orchestrator struct {
	store     store.Store
	config    *config.Config
	bus       *notify.EventBus
	docker    Runner
	outputs   Writer
	embedder  embeddings.Embedder
	lifecycle *lifecycle.Coordinator

	mu              sync.Mutex
	running         int
	stopCh          chan struct{}
	inspectFailures map[string]int // task ID -> consecutive inspect failure count
}

func New(s store.Store, cfg *config.Config, bus *notify.EventBus, runner Runner, outputs Writer, embedder embeddings.Embedder) *Orchestrator {
	o := &Orchestrator{
		store:           s,
		config:          cfg,
		bus:             bus,
		docker:          runner,
		outputs:         outputs,
		embedder:        embedder,
		stopCh:          make(chan struct{}),
		inspectFailures: make(map[string]int),
	}
	o.lifecycle = lifecycle.New(s, bus,
		lifecycle.WithSlots(slotsAdapter{o: o}),
		lifecycle.WithMaxUserRetries(cfg.MaxUserRetries),
	)

	o.initInstance()

	return o
}

// slotsAdapter exposes the orchestrator's running-counter helpers through
// the narrow lifecycle.Slots interface. Used to break the construction cycle
// between Orchestrator and Coordinator while keeping the counter logic in
// one place.
type slotsAdapter struct {
	o *Orchestrator
}

func (a slotsAdapter) Acquire() {
	a.o.incrementRunning()
}

func (a slotsAdapter) Release(ctx context.Context, task *models.Task) {
	a.o.releaseSlot(ctx, task)
}

// initInstance ensures the synthetic local instance row exists and is marked
// running with zero containers. This is the only instance the orchestrator
// tracks now that it runs containers directly on the local Docker host.
func (o *Orchestrator) initInstance() {
	ctx := context.Background()

	_, err := o.store.GetInstance(ctx, localInstanceID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		now := time.Now().UTC()
		inst := &models.Instance{
			InstanceID:    localInstanceID,
			Status:        models.InstanceStatusRunning,
			MaxContainers: o.config.ContainersPerInst,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if err := o.store.CreateInstance(ctx, inst); err != nil {
			log.Error().Err(err).Msg("init: failed to create synthetic local instance")
		}
	case err != nil:
		log.Error().Err(err).Msg("init: failed to get synthetic local instance")
	default:
		o.store.UpdateInstanceStatus(ctx, localInstanceID, models.InstanceStatusRunning)
		o.store.ResetRunningContainers(ctx, localInstanceID)
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

// releaseInstanceSlot decrements the running container count for an instance.
func (o *Orchestrator) releaseInstanceSlot(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if err := o.store.DecrementRunningContainers(ctx, instanceID); err != nil {
		log.Warn().Err(err).Str("instance_id", instanceID).Msg("releaseInstanceSlot: failed to decrement running containers")
	}
}

// releaseSlot decrements both the running counter and the instance container count.
func (o *Orchestrator) releaseSlot(ctx context.Context, task *models.Task) {
	o.decrementRunning()
	o.releaseInstanceSlot(ctx, task.InstanceID)
}
