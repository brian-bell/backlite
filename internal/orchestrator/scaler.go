package orchestrator

import "context"

// Scaler abstracts instance scaling decisions.
type Scaler interface {
	Evaluate(ctx context.Context)
	RequestScaleUp(ctx context.Context)
}
