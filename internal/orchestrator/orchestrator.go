package orchestrator

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

// maxInspectFailures is the number of consecutive container inspect failures
// before a task is killed or requeued.
const maxInspectFailures = 3

// Orchestrator manages the lifecycle of tasks: dispatching them to instances,
// monitoring their containers, handling completions, and recovering from restarts.
type Orchestrator struct {
	store  store.Store
	config *config.Config
	bus    *notify.EventBus
	docker Runner
	scaler Scaler
	spot   SpotChecker
	s3     S3Client

	mu              sync.Mutex
	running         int
	stopCh          chan struct{}
	inspectFailures map[string]int // task ID -> consecutive inspect failure count
}

func New(s store.Store, cfg *config.Config, bus *notify.EventBus, runner Runner, scaler Scaler, spot SpotChecker, s3 S3Client) *Orchestrator {
	o := &Orchestrator{
		store:           s,
		config:          cfg,
		bus:             bus,
		docker:          runner,
		scaler:          scaler,
		spot:            spot,
		s3:              s3,
		stopCh:          make(chan struct{}),
		inspectFailures: make(map[string]int),
	}

	switch cfg.Mode {
	case config.ModeLocal:
		o.initLocalMode()
	case config.ModeFargate:
		o.initFargateMode()
	default:
		o.initEC2Mode()
	}

	return o
}

// initLocalMode seeds a "local" instance so findAvailableInstance works without EC2.
func (o *Orchestrator) initLocalMode() {
	o.syncSyntheticInstance(syntheticInstanceSpec{
		id:            "local",
		instanceType:  "local",
		maxContainers: o.config.ContainersPerInst,
		privateIP:     "127.0.0.1",
		getErrMsg:     "local init: failed to get synthetic instance",
		createErrMsg:  "local init: failed to create synthetic instance",
	})
}

// initFargateMode seeds a synthetic "fargate" instance so the orchestrator can
// track capacity without managing VM lifecycle.
func (o *Orchestrator) initFargateMode() {
	if !o.syncSyntheticInstance(syntheticInstanceSpec{
		id:            "fargate",
		instanceType:  "fargate",
		maxContainers: o.config.MaxConcurrentTasks,
		getErrMsg:     "fargate init: failed to get synthetic instance",
		createErrMsg:  "fargate init: failed to create synthetic instance",
	}) {
		return
	}
	o.terminateStaleInstances("fargate")
}

// initEC2Mode cleans up leftover local instances from a previous local-mode run.
func (o *Orchestrator) initEC2Mode() {
	inst, err := o.store.GetInstance(context.Background(), "local")
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Error().Err(err).Msg("ec2 init: failed to check for leftover local instance")
	} else if err == nil && inst.Status != models.InstanceStatusTerminated {
		o.store.UpdateInstanceStatus(context.Background(), "local", models.InstanceStatusTerminated)
	}
}

type syntheticInstanceSpec struct {
	id            string
	instanceType  string
	maxContainers int
	privateIP     string
	getErrMsg     string
	createErrMsg  string
}

// syncSyntheticInstance ensures a synthetic instance exists and is marked running.
// It is used by local and Fargate modes to keep capacity management consistent.
func (o *Orchestrator) syncSyntheticInstance(spec syntheticInstanceSpec) bool {
	ctx := context.Background()

	_, err := o.store.GetInstance(ctx, spec.id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		now := time.Now().UTC()
		inst := &models.Instance{
			InstanceID:    spec.id,
			InstanceType:  spec.instanceType,
			Status:        models.InstanceStatusRunning,
			MaxContainers: spec.maxContainers,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if spec.privateIP != "" {
			inst.PrivateIP = spec.privateIP
		}
		if err := o.store.CreateInstance(ctx, inst); err != nil && spec.createErrMsg != "" {
			log.Error().Err(err).Msg(spec.createErrMsg)
		}
		return true
	case err != nil:
		if spec.getErrMsg != "" {
			log.Error().Err(err).Msg(spec.getErrMsg)
		}
		return false
	default:
		o.store.UpdateInstanceStatus(ctx, spec.id, models.InstanceStatusRunning)
		o.store.ResetRunningContainers(ctx, spec.id)
		return true
	}
}

// terminateStaleInstances marks any non-synthetic instances as terminated.
func (o *Orchestrator) terminateStaleInstances(keepID string) {
	ctx := context.Background()

	instances, err := o.store.ListInstances(ctx, nil)
	if err != nil {
		log.Error().Err(err).Msg("fargate init: failed to list instances for cleanup")
		return
	}
	for _, other := range instances {
		if other.InstanceID == keepID || other.Status == models.InstanceStatusTerminated {
			continue
		}
		if err := o.store.UpdateInstanceStatus(ctx, other.InstanceID, models.InstanceStatusTerminated); err != nil {
			log.Error().Err(err).Str("instance_id", other.InstanceID).Msg("fargate init: failed to terminate stale instance")
		}
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
		Str("mode", string(o.config.Mode)).
		Str("auth_mode", string(o.config.AuthMode)).
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

// tick runs a single orchestration cycle: monitor, dispatch, scale.
func (o *Orchestrator) tick(ctx context.Context) {
	if o.spot != nil {
		o.spot.CheckInterruptions(ctx)
	}
	o.monitorCancelled(ctx)
	o.monitorRecovering(ctx)
	o.monitorRunning(ctx)
	o.dispatchPending(ctx)
	o.scaler.Evaluate(ctx)
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

// markInstanceTerminated sets an instance to terminated status if it isn't already.
func (o *Orchestrator) markInstanceTerminated(ctx context.Context, instanceID string) {
	if instanceID == "" {
		return
	}
	if err := o.store.UpdateInstanceStatus(ctx, instanceID, models.InstanceStatusTerminated); err != nil {
		log.Warn().Err(err).Str("instance_id", instanceID).Msg("markInstanceTerminated: failed to update instance status")
	}
}
