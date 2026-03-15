package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/notify"
	"github.com/backflow-labs/backflow/internal/store"
)

type Orchestrator struct {
	store    store.Store
	config   *config.Config
	notifier notify.Notifier
	ec2      *EC2Manager
	docker   *DockerManager
	scaler   scaler
	spot     *SpotHandler

	mu              sync.Mutex
	running         int
	stopCh          chan struct{}
	inspectFailures map[string]int // task ID -> consecutive inspect failure count
}

func New(s store.Store, cfg *config.Config, notifier notify.Notifier) *Orchestrator {
	docker := NewDockerManager(cfg)

	o := &Orchestrator{
		store:           s,
		config:          cfg,
		notifier:        notifier,
		docker:          docker,
		stopCh:          make(chan struct{}),
		inspectFailures: make(map[string]int),
	}

	if cfg.Mode == config.ModeLocal {
		o.scaler = localScaler{}
		// Seed a local instance so findAvailableInstance works without EC2.
		// If it already exists (server restart), reset it to running state.
		now := time.Now().UTC()
		inst, _ := s.GetInstance(context.Background(), "local")
		if inst != nil {
			inst.Status = models.InstanceStatusRunning
			inst.MaxContainers = cfg.ContainersPerInst
			inst.RunningContainers = 0
			inst.UpdatedAt = now
			s.UpdateInstance(context.Background(), inst)
		} else {
			inst = &models.Instance{
				InstanceID:    "local",
				InstanceType:  "local",
				Status:        models.InstanceStatusRunning,
				MaxContainers: cfg.ContainersPerInst,
				PrivateIP:     "127.0.0.1",
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			s.CreateInstance(context.Background(), inst)
		}
	} else {
		ec2 := NewEC2Manager(cfg)
		o.ec2 = ec2
		o.scaler = NewScaler(s, ec2, docker, cfg)
		o.spot = NewSpotHandler(s, ec2)

		// Clean up leftover local instance from a previous local-mode run.
		if inst, _ := s.GetInstance(context.Background(), "local"); inst != nil && inst.Status != models.InstanceStatusTerminated {
			inst.Status = models.InstanceStatusTerminated
			inst.RunningContainers = 0
			s.UpdateInstance(context.Background(), inst)
		}
	}

	return o
}

// Docker returns the DockerManager for use by the API logs endpoint.
func (o *Orchestrator) Docker() *DockerManager {
	return o.docker
}

func (o *Orchestrator) Start(ctx context.Context) {
	log.Info().
		Str("mode", string(o.config.Mode)).
		Str("auth_mode", string(o.config.AuthMode)).
		Int("max_concurrent", o.config.MaxConcurrent()).
		Dur("poll_interval", o.config.PollInterval).
		Msg("orchestrator started")

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

func (o *Orchestrator) Stop() {
	close(o.stopCh)
}

func (o *Orchestrator) tick(ctx context.Context) {
	o.monitorRunning(ctx)
	o.dispatchPending(ctx)
	o.scaler.Evaluate(ctx)
}

func (o *Orchestrator) dispatchPending(ctx context.Context) {
	o.mu.Lock()
	maxConcurrent := o.config.MaxConcurrent()
	available := maxConcurrent - o.running
	o.mu.Unlock()

	if available <= 0 {
		return
	}

	pending := models.TaskStatusPending
	tasks, err := o.store.ListTasks(ctx, store.TaskFilter{
		Status: &pending,
		Limit:  available,
	})
	if err != nil {
		log.Error().Err(err).Msg("failed to list pending tasks")
		return
	}

	for _, task := range tasks {
		if err := o.dispatch(ctx, task); err != nil {
			log.Error().Err(err).Str("task_id", task.ID).Msg("failed to dispatch task")
			task.Status = models.TaskStatusFailed
			task.Error = err.Error()
			o.store.UpdateTask(ctx, task)
			o.notifier.Notify(notify.Event{
				Type:      notify.EventTaskFailed,
				TaskID:    task.ID,
				RepoURL:   task.RepoURL,
				Prompt:    task.Prompt,
				Message:   "Failed to dispatch: " + err.Error(),
				Timestamp: time.Now().UTC(),
			})
			continue
		}
	}
}

func (o *Orchestrator) dispatch(ctx context.Context, task *models.Task) error {
	// Find an instance with capacity
	instance, err := o.findAvailableInstance(ctx)
	if err != nil {
		// Request scale-up and re-queue for next tick
		o.scaler.RequestScaleUp(ctx)
		return nil
	}

	// Update task status to provisioning
	task.Status = models.TaskStatusProvisioning
	task.InstanceID = instance.InstanceID
	if err := o.store.UpdateTask(ctx, task); err != nil {
		return err
	}

	// Start container
	containerID, err := o.docker.RunAgent(ctx, instance, task)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	task.Status = models.TaskStatusRunning
	task.ContainerID = containerID
	task.StartedAt = &now
	if err := o.store.UpdateTask(ctx, task); err != nil {
		return err
	}

	o.mu.Lock()
	o.running++
	o.mu.Unlock()

	// Update instance container count
	instance.RunningContainers++
	o.store.UpdateInstance(ctx, instance)

	o.notifier.Notify(notify.Event{
		Type:      notify.EventTaskRunning,
		TaskID:    task.ID,
		RepoURL:   task.RepoURL,
		Prompt:    task.Prompt,
		Timestamp: now,
	})

	log.Info().Str("task_id", task.ID).Str("container", containerID).Str("instance", instance.InstanceID).Msg("task dispatched")
	return nil
}

func (o *Orchestrator) findAvailableInstance(ctx context.Context) (*models.Instance, error) {
	running := models.InstanceStatusRunning
	instances, err := o.store.ListInstances(ctx, &running)
	if err != nil {
		return nil, err
	}

	for _, inst := range instances {
		if inst.RunningContainers < inst.MaxContainers {
			return inst, nil
		}
	}

	return nil, errNoCapacity
}

func (o *Orchestrator) monitorRunning(ctx context.Context) {
	running := models.TaskStatusRunning
	tasks, err := o.store.ListTasks(ctx, store.TaskFilter{Status: &running})
	if err != nil {
		log.Error().Err(err).Msg("failed to list running tasks")
		return
	}

	for _, task := range tasks {
		// Check timeout
		if task.StartedAt != nil && task.MaxRuntimeMin > 0 {
			deadline := task.StartedAt.Add(time.Duration(task.MaxRuntimeMin) * time.Minute)
			if time.Now().UTC().After(deadline) {
				log.Warn().Str("task_id", task.ID).Msg("task exceeded max runtime, killing")
				o.killTask(ctx, task, "exceeded max runtime")
				continue
			}
		}

		// Check container status
		status, err := o.docker.InspectContainer(ctx, task.InstanceID, task.ContainerID)
		if err != nil {
			if isInstanceGone(err) {
				log.Warn().Err(err).Str("task_id", task.ID).Str("instance", task.InstanceID).Msg("instance terminated, re-queuing task")
				delete(o.inspectFailures, task.ID)
				o.requeueTask(ctx, task, "instance terminated")
				continue
			}
			o.inspectFailures[task.ID]++
			count := o.inspectFailures[task.ID]
			log.Warn().Err(err).Str("task_id", task.ID).Int("consecutive_failures", count).Msg("failed to inspect container")
			if count >= 3 {
				delete(o.inspectFailures, task.ID)
				o.killTask(ctx, task, fmt.Sprintf("container unreachable after %d inspect failures: %v", count, err))
			}
			continue
		}

		delete(o.inspectFailures, task.ID)

		if status.Done {
			o.handleCompletion(ctx, task, status)
		}
	}
}

func (o *Orchestrator) handleCompletion(ctx context.Context, task *models.Task, status ContainerStatus) {
	now := time.Now().UTC()
	task.CompletedAt = &now

	task.PRURL = status.PRURL

	if status.ExitCode == 0 {
		task.Status = models.TaskStatusCompleted
		o.notifier.Notify(notify.Event{
			Type:         notify.EventTaskCompleted,
			TaskID:       task.ID,
			RepoURL:      task.RepoURL,
			Prompt:       task.Prompt,
			PRURL:        status.PRURL,
			AgentLogTail: status.LogTail,
			Timestamp:    now,
		})
	} else if status.NeedsInput {
		task.Status = models.TaskStatusFailed
		task.Error = "agent needs input"
		o.notifier.Notify(notify.Event{
			Type:         notify.EventTaskNeedsInput,
			TaskID:       task.ID,
			RepoURL:      task.RepoURL,
			Prompt:       task.Prompt,
			Message:      status.Question,
			AgentLogTail: status.LogTail,
			Timestamp:    now,
		})
	} else {
		task.Status = models.TaskStatusFailed
		task.Error = status.Error
		o.notifier.Notify(notify.Event{
			Type:         notify.EventTaskFailed,
			TaskID:       task.ID,
			RepoURL:      task.RepoURL,
			Prompt:       task.Prompt,
			Message:      status.Error,
			AgentLogTail: status.LogTail,
			Timestamp:    now,
		})
	}

	o.store.UpdateTask(ctx, task)

	o.mu.Lock()
	o.running--
	o.mu.Unlock()

	// Decrement instance container count
	if task.InstanceID != "" {
		if inst, err := o.store.GetInstance(ctx, task.InstanceID); err == nil && inst != nil {
			inst.RunningContainers--
			if inst.RunningContainers < 0 {
				inst.RunningContainers = 0
			}
			o.store.UpdateInstance(ctx, inst)
		}
	}

	log.Info().Str("task_id", task.ID).Str("status", string(task.Status)).Msg("task completed")
}

func (o *Orchestrator) killTask(ctx context.Context, task *models.Task, reason string) {
	if task.ContainerID != "" {
		o.docker.StopContainer(ctx, task.InstanceID, task.ContainerID)
	}

	now := time.Now().UTC()
	task.Status = models.TaskStatusFailed
	task.Error = reason
	task.CompletedAt = &now
	o.store.UpdateTask(ctx, task)

	o.mu.Lock()
	o.running--
	o.mu.Unlock()

	if task.InstanceID != "" {
		if inst, err := o.store.GetInstance(ctx, task.InstanceID); err == nil && inst != nil {
			inst.RunningContainers--
			if inst.RunningContainers < 0 {
				inst.RunningContainers = 0
			}
			o.store.UpdateInstance(ctx, inst)
		}
	}

	o.notifier.Notify(notify.Event{
		Type:      notify.EventTaskFailed,
		TaskID:    task.ID,
		RepoURL:   task.RepoURL,
		Prompt:    task.Prompt,
		Message:   reason,
		Timestamp: now,
	})
}

// requeueTask resets a running task back to pending so it will be dispatched
// to a different instance. It also marks the old instance as terminated.
func (o *Orchestrator) requeueTask(ctx context.Context, task *models.Task, reason string) {
	// Mark the instance as terminated so no new tasks get dispatched to it.
	if task.InstanceID != "" {
		if inst, err := o.store.GetInstance(ctx, task.InstanceID); err == nil && inst != nil {
			if inst.Status != models.InstanceStatusTerminated {
				inst.Status = models.InstanceStatusTerminated
				inst.RunningContainers = 0
				o.store.UpdateInstance(ctx, inst)
			}
		}
	}

	o.mu.Lock()
	o.running--
	o.mu.Unlock()

	task.Status = models.TaskStatusPending
	task.InstanceID = ""
	task.ContainerID = ""
	task.StartedAt = nil
	task.Error = "re-queued: " + reason + " at " + time.Now().UTC().Format(time.RFC3339)
	task.RetryCount++
	if err := o.store.UpdateTask(ctx, task); err != nil {
		log.Error().Err(err).Str("task_id", task.ID).Msg("failed to re-queue task")
	}

	// Trigger scale-up so a new instance is provisioned.
	o.scaler.RequestScaleUp(ctx)
}

var errNoCapacity = fmt.Errorf("no instance capacity available")
