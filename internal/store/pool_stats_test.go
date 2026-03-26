//go:build !nocontainers

package store

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPoolStats_ReturnsValidStats(t *testing.T) {
	ctx := context.Background()
	_, thisFile, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")

	s, err := NewPostgres(ctx, sharedConnStr, migrationsDir)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	defer s.Close()

	stats := s.PoolStats()

	if stats.MaxConns <= 0 {
		t.Errorf("MaxConns = %d, want > 0", stats.MaxConns)
	}
	if stats.TotalConns < 0 {
		t.Errorf("TotalConns = %d, want >= 0", stats.TotalConns)
	}
	if stats.AcquiredConns < 0 {
		t.Errorf("AcquiredConns = %d, want >= 0", stats.AcquiredConns)
	}
	if stats.IdleConns < 0 {
		t.Errorf("IdleConns = %d, want >= 0", stats.IdleConns)
	}
}
