package ec2

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/backflow-labs/backflow/internal/config"
	"github.com/backflow-labs/backflow/internal/models"
	"github.com/backflow-labs/backflow/internal/store"
)

// --- mock ec2Client ---

type mockEC2Client struct {
	launchID  string
	launchErr error

	terminateCalls []string
	terminateErr   error

	// Per-instance describe results. Falls back to describeDefault if set.
	describeResults map[string]*types.Instance
	describeDefault *types.Instance
	describeErr     error
}

func (m *mockEC2Client) LaunchSpotInstance(_ context.Context) (string, error) {
	return m.launchID, m.launchErr
}

func (m *mockEC2Client) TerminateInstance(_ context.Context, instanceID string) error {
	m.terminateCalls = append(m.terminateCalls, instanceID)
	return m.terminateErr
}

func (m *mockEC2Client) DescribeInstance(_ context.Context, instanceID string) (*types.Instance, error) {
	if m.describeErr != nil {
		return nil, m.describeErr
	}
	if inst, ok := m.describeResults[instanceID]; ok {
		return inst, nil
	}
	if m.describeDefault != nil {
		return m.describeDefault, nil
	}
	return nil, fmt.Errorf("instance %s not found", instanceID)
}

// --- mock store ---

type statusUpdate struct {
	instanceID string
	status     models.InstanceStatus
}

type detailsUpdate struct {
	instanceID string
	ip, az     string
}

type mockScalerStore struct {
	store.Store // embed to satisfy interface; panics on unimplemented methods
	instances   []*models.Instance

	createInstanceErr error
	createCalls       int

	statusUpdates  []statusUpdate
	detailsUpdates []detailsUpdate
}

func (m *mockScalerStore) ListInstances(_ context.Context, status *models.InstanceStatus) ([]*models.Instance, error) {
	if status == nil {
		return m.instances, nil
	}
	var result []*models.Instance
	for _, inst := range m.instances {
		if inst.Status == *status {
			result = append(result, inst)
		}
	}
	return result, nil
}

func (m *mockScalerStore) CreateInstance(_ context.Context, inst *models.Instance) error {
	m.createCalls++
	if m.createInstanceErr != nil {
		return m.createInstanceErr
	}
	m.instances = append(m.instances, inst)
	return nil
}

func (m *mockScalerStore) UpdateInstanceStatus(_ context.Context, instanceID string, status models.InstanceStatus) error {
	m.statusUpdates = append(m.statusUpdates, statusUpdate{instanceID, status})
	// Also update the in-memory instance so subsequent ListInstances reflects changes.
	for _, inst := range m.instances {
		if inst.InstanceID == instanceID {
			inst.Status = status
		}
	}
	return nil
}

func (m *mockScalerStore) UpdateInstanceDetails(_ context.Context, instanceID, ip, az string) error {
	m.detailsUpdates = append(m.detailsUpdates, detailsUpdate{instanceID, ip, az})
	return nil
}

func (m *mockScalerStore) findStatusUpdate(instanceID string) *statusUpdate {
	for i := range m.statusUpdates {
		if m.statusUpdates[i].instanceID == instanceID {
			return &m.statusUpdates[i]
		}
	}
	return nil
}

func newTestScaler(ec2mock *mockEC2Client, db *mockScalerStore) *Scaler {
	cfg := &config.Config{
		MaxInstances:      3,
		InstanceType:      "c5.xlarge",
		ContainersPerInst: 2,
	}
	return &Scaler{store: db, ec2: ec2mock, config: cfg}
}

// ==================== RequestScaleUp ====================

func TestRequestScaleUp_Success(t *testing.T) {
	ec2mock := &mockEC2Client{launchID: "i-good456"}
	db := &mockScalerStore{}
	s := newTestScaler(ec2mock, db)

	s.RequestScaleUp(context.Background())

	if db.createCalls != 1 {
		t.Fatalf("CreateInstance calls = %d, want 1", db.createCalls)
	}
	if len(ec2mock.terminateCalls) != 0 {
		t.Errorf("TerminateInstance calls = %d, want 0", len(ec2mock.terminateCalls))
	}
	if len(db.instances) != 1 || db.instances[0].InstanceID != "i-good456" {
		t.Errorf("saved instance = %v, want i-good456", db.instances)
	}
	inst := db.instances[0]
	if inst.Status != models.InstanceStatusPending {
		t.Errorf("status = %q, want pending", inst.Status)
	}
	if inst.InstanceType != "c5.xlarge" {
		t.Errorf("instance_type = %q, want c5.xlarge", inst.InstanceType)
	}
	if inst.MaxContainers != 2 {
		t.Errorf("max_containers = %d, want 2", inst.MaxContainers)
	}
}

func TestRequestScaleUp_TerminatesOnDBFailure(t *testing.T) {
	ec2mock := &mockEC2Client{launchID: "i-orphan123"}
	db := &mockScalerStore{createInstanceErr: fmt.Errorf("connection refused")}
	s := newTestScaler(ec2mock, db)

	s.RequestScaleUp(context.Background())

	if len(ec2mock.terminateCalls) != 1 {
		t.Fatalf("TerminateInstance calls = %d, want 1", len(ec2mock.terminateCalls))
	}
	if ec2mock.terminateCalls[0] != "i-orphan123" {
		t.Errorf("terminated instance = %q, want %q", ec2mock.terminateCalls[0], "i-orphan123")
	}
}

func TestRequestScaleUp_SkipsWhenAtMax(t *testing.T) {
	ec2mock := &mockEC2Client{launchID: "i-shouldnt-launch"}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-1", Status: models.InstanceStatusRunning},
			{InstanceID: "i-2", Status: models.InstanceStatusRunning},
			{InstanceID: "i-3", Status: models.InstanceStatusRunning},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.RequestScaleUp(context.Background())

	if db.createCalls != 0 {
		t.Errorf("CreateInstance calls = %d, want 0", db.createCalls)
	}
}

func TestRequestScaleUp_SkipsWhenPendingExists(t *testing.T) {
	ec2mock := &mockEC2Client{launchID: "i-shouldnt-launch"}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-pending", Status: models.InstanceStatusPending},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.RequestScaleUp(context.Background())

	if db.createCalls != 0 {
		t.Errorf("CreateInstance calls = %d, want 0 (pending exists)", db.createCalls)
	}
}

func TestRequestScaleUp_LaunchFailure(t *testing.T) {
	ec2mock := &mockEC2Client{launchErr: fmt.Errorf("insufficient capacity")}
	db := &mockScalerStore{}
	s := newTestScaler(ec2mock, db)

	s.RequestScaleUp(context.Background())

	if db.createCalls != 0 {
		t.Errorf("CreateInstance calls = %d, want 0", db.createCalls)
	}
	if len(ec2mock.terminateCalls) != 0 {
		t.Errorf("TerminateInstance calls = %d, want 0", len(ec2mock.terminateCalls))
	}
}

func TestRequestScaleUp_IgnoresTerminatedInstances(t *testing.T) {
	ec2mock := &mockEC2Client{launchID: "i-new"}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-old1", Status: models.InstanceStatusTerminated},
			{InstanceID: "i-old2", Status: models.InstanceStatusTerminated},
			{InstanceID: "i-old3", Status: models.InstanceStatusTerminated},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.RequestScaleUp(context.Background())

	// Terminated instances don't count toward active, so launch should proceed.
	if db.createCalls != 1 {
		t.Errorf("CreateInstance calls = %d, want 1", db.createCalls)
	}
}

// ==================== reconcileRunning ====================

func TestReconcileRunning_DetectsTerminatedInstance(t *testing.T) {
	ec2mock := &mockEC2Client{
		describeResults: map[string]*types.Instance{
			"i-gone": {State: &types.InstanceState{Name: types.InstanceStateNameTerminated}},
		},
	}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-gone", Status: models.InstanceStatusRunning},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcileRunning(context.Background())

	u := db.findStatusUpdate("i-gone")
	if u == nil {
		t.Fatal("expected status update for i-gone")
	}
	if u.status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", u.status)
	}
}

func TestReconcileRunning_DetectsShuttingDown(t *testing.T) {
	ec2mock := &mockEC2Client{
		describeResults: map[string]*types.Instance{
			"i-dying": {State: &types.InstanceState{Name: types.InstanceStateNameShuttingDown}},
		},
	}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-dying", Status: models.InstanceStatusRunning},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcileRunning(context.Background())

	u := db.findStatusUpdate("i-dying")
	if u == nil {
		t.Fatal("expected status update for i-dying")
	}
	if u.status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", u.status)
	}
}

func TestReconcileRunning_StillRunningNoOp(t *testing.T) {
	ec2mock := &mockEC2Client{
		describeResults: map[string]*types.Instance{
			"i-healthy": {State: &types.InstanceState{Name: types.InstanceStateNameRunning}},
		},
	}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-healthy", Status: models.InstanceStatusRunning},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcileRunning(context.Background())

	if len(db.statusUpdates) != 0 {
		t.Errorf("status updates = %d, want 0 (instance still healthy)", len(db.statusUpdates))
	}
}

func TestReconcileRunning_DescribeFailsMarksTerminated(t *testing.T) {
	ec2mock := &mockEC2Client{describeErr: fmt.Errorf("API error")}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-unreachable", Status: models.InstanceStatusRunning},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcileRunning(context.Background())

	u := db.findStatusUpdate("i-unreachable")
	if u == nil {
		t.Fatal("expected status update for i-unreachable")
	}
	if u.status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", u.status)
	}
}

// ==================== reconcilePending ====================

func TestReconcilePending_TerminatedInstance(t *testing.T) {
	ec2mock := &mockEC2Client{
		describeResults: map[string]*types.Instance{
			"i-dead": {State: &types.InstanceState{Name: types.InstanceStateNameTerminated}},
		},
	}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-dead", Status: models.InstanceStatusPending, CreatedAt: time.Now()},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcilePending(context.Background())

	u := db.findStatusUpdate("i-dead")
	if u == nil {
		t.Fatal("expected status update for i-dead")
	}
	if u.status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", u.status)
	}
}

func TestReconcilePending_ShuttingDownInstance(t *testing.T) {
	ec2mock := &mockEC2Client{
		describeResults: map[string]*types.Instance{
			"i-dying": {State: &types.InstanceState{Name: types.InstanceStateNameShuttingDown}},
		},
	}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-dying", Status: models.InstanceStatusPending, CreatedAt: time.Now()},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcilePending(context.Background())

	u := db.findStatusUpdate("i-dying")
	if u == nil {
		t.Fatal("expected status update for i-dying")
	}
	if u.status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", u.status)
	}
}

func TestReconcilePending_StillPendingNoOp(t *testing.T) {
	ec2mock := &mockEC2Client{
		describeResults: map[string]*types.Instance{
			"i-booting": {State: &types.InstanceState{Name: types.InstanceStateNamePending}},
		},
	}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-booting", Status: models.InstanceStatusPending, CreatedAt: time.Now()},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcilePending(context.Background())

	if len(db.statusUpdates) != 0 {
		t.Errorf("status updates = %d, want 0 (still pending in EC2)", len(db.statusUpdates))
	}
}

func TestReconcilePending_DescribeFailsOldInstance(t *testing.T) {
	ec2mock := &mockEC2Client{describeErr: fmt.Errorf("not found")}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-stale", Status: models.InstanceStatusPending, CreatedAt: time.Now().Add(-10 * time.Minute)},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcilePending(context.Background())

	// Old pending instance (>5min) that can't be described → mark terminated.
	u := db.findStatusUpdate("i-stale")
	if u == nil {
		t.Fatal("expected status update for i-stale")
	}
	if u.status != models.InstanceStatusTerminated {
		t.Errorf("status = %q, want terminated", u.status)
	}
}

func TestReconcilePending_DescribeFailsNewInstance(t *testing.T) {
	ec2mock := &mockEC2Client{describeErr: fmt.Errorf("not found")}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-fresh", Status: models.InstanceStatusPending, CreatedAt: time.Now().Add(-1 * time.Minute)},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.reconcilePending(context.Background())

	// New pending instance (<5min) that can't be described → leave as pending, give it time.
	if len(db.statusUpdates) != 0 {
		t.Errorf("status updates = %d, want 0 (new instance, give it time)", len(db.statusUpdates))
	}
}

// ==================== scaleDown ====================

func TestScaleDown_TerminatesIdleInstance(t *testing.T) {
	longAgo := time.Now().Add(-10 * time.Minute)
	ec2mock := &mockEC2Client{}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-idle", Status: models.InstanceStatusRunning, RunningContainers: 0, UpdatedAt: longAgo},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.scaleDown(context.Background())

	if len(ec2mock.terminateCalls) != 1 {
		t.Fatalf("TerminateInstance calls = %d, want 1", len(ec2mock.terminateCalls))
	}
	if ec2mock.terminateCalls[0] != "i-idle" {
		t.Errorf("terminated = %q, want i-idle", ec2mock.terminateCalls[0])
	}
	u := db.findStatusUpdate("i-idle")
	if u == nil || u.status != models.InstanceStatusTerminated {
		t.Error("expected status update to terminated")
	}
}

func TestScaleDown_KeepsActiveInstance(t *testing.T) {
	longAgo := time.Now().Add(-10 * time.Minute)
	ec2mock := &mockEC2Client{}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-busy", Status: models.InstanceStatusRunning, RunningContainers: 1, UpdatedAt: longAgo},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.scaleDown(context.Background())

	if len(ec2mock.terminateCalls) != 0 {
		t.Errorf("TerminateInstance calls = %d, want 0 (has running containers)", len(ec2mock.terminateCalls))
	}
}

func TestScaleDown_KeepsRecentlyActiveInstance(t *testing.T) {
	recent := time.Now().Add(-1 * time.Minute)
	ec2mock := &mockEC2Client{}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-recent", Status: models.InstanceStatusRunning, RunningContainers: 0, UpdatedAt: recent},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.scaleDown(context.Background())

	if len(ec2mock.terminateCalls) != 0 {
		t.Errorf("TerminateInstance calls = %d, want 0 (recently active)", len(ec2mock.terminateCalls))
	}
}

func TestScaleDown_TerminateFailsContinues(t *testing.T) {
	longAgo := time.Now().Add(-10 * time.Minute)
	ec2mock := &mockEC2Client{terminateErr: fmt.Errorf("API throttled")}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-idle1", Status: models.InstanceStatusRunning, RunningContainers: 0, UpdatedAt: longAgo},
			{InstanceID: "i-idle2", Status: models.InstanceStatusRunning, RunningContainers: 0, UpdatedAt: longAgo},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.scaleDown(context.Background())

	// Both should be attempted even though terminate fails.
	if len(ec2mock.terminateCalls) != 2 {
		t.Errorf("TerminateInstance calls = %d, want 2 (should continue on error)", len(ec2mock.terminateCalls))
	}
	// Status should NOT be updated since terminate failed.
	if len(db.statusUpdates) != 0 {
		t.Errorf("status updates = %d, want 0 (terminate failed, don't mark terminated)", len(db.statusUpdates))
	}
}

func TestScaleDown_MixedInstances(t *testing.T) {
	longAgo := time.Now().Add(-10 * time.Minute)
	recent := time.Now().Add(-1 * time.Minute)
	ec2mock := &mockEC2Client{}
	db := &mockScalerStore{
		instances: []*models.Instance{
			{InstanceID: "i-idle", Status: models.InstanceStatusRunning, RunningContainers: 0, UpdatedAt: longAgo},
			{InstanceID: "i-busy", Status: models.InstanceStatusRunning, RunningContainers: 2, UpdatedAt: longAgo},
			{InstanceID: "i-warm", Status: models.InstanceStatusRunning, RunningContainers: 0, UpdatedAt: recent},
		},
	}
	s := newTestScaler(ec2mock, db)

	s.scaleDown(context.Background())

	// Only the idle one should be terminated.
	if len(ec2mock.terminateCalls) != 1 {
		t.Fatalf("TerminateInstance calls = %d, want 1", len(ec2mock.terminateCalls))
	}
	if ec2mock.terminateCalls[0] != "i-idle" {
		t.Errorf("terminated = %q, want i-idle", ec2mock.terminateCalls[0])
	}
}
