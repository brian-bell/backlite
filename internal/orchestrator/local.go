package orchestrator

import "context"

// NoopScaler is a no-op scaler used in local and Fargate modes where there
// are no EC2 instances to manage.
type NoopScaler struct{}

func (NoopScaler) Evaluate(ctx context.Context)       {}
func (NoopScaler) RequestScaleUp(ctx context.Context) {}
