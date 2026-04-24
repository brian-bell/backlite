package api

import (
	"context"

	"github.com/brian-bell/backlite/internal/notify"
)

type noopLogFetcher struct{}

func (noopLogFetcher) GetLogs(_ context.Context, _, _ string, _ int) (string, error) {
	return "test logs\n", nil
}

type noopEmitter struct{}

func (noopEmitter) Emit(_ notify.Event) {}
