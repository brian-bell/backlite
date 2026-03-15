package orchestrator

import "context"

// localScaler is a no-op scaler used in local mode where there are no EC2
// instances to manage.
type localScaler struct{}

func (localScaler) Evaluate(ctx context.Context)      {}
func (localScaler) RequestScaleUp(ctx context.Context) {}
