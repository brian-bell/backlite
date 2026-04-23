package store

import (
	"testing"
)

func TestPoolStats_ReturnsValidStats(t *testing.T) {
	s := testSQLiteStore(t)

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
