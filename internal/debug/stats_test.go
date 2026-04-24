package debug

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brian-bell/backlite/internal/store"
)

type mockPoolStatter struct{}

func (mockPoolStatter) PoolStats() store.PoolStats {
	return store.PoolStats{
		AcquiredConns: 2,
		IdleConns:     3,
		TotalConns:    5,
		MaxConns:      10,
	}
}

func TestStatsHandler_ReturnsExpectedFields(t *testing.T) {
	startedAt := time.Now().Add(-10 * time.Second)
	handler := StatsHandler(func() int { return 3 }, mockPoolStatter{}, startedAt)

	req := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Orchestrator struct {
				RunningTasks int `json:"running_tasks"`
			} `json:"orchestrator"`
			Pool struct {
				AcquiredConns int32 `json:"acquired_conns"`
				IdleConns     int32 `json:"idle_conns"`
				TotalConns    int32 `json:"total_conns"`
				MaxConns      int32 `json:"max_conns"`
			} `json:"pool"`
			UptimeSeconds float64 `json:"uptime_seconds"`
			Runtime       struct {
				HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
				SysBytes       uint64 `json:"sys_bytes"`
			} `json:"runtime"`
			PID int `json:"pid"`
		} `json:"data"`
	}

	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Data.Orchestrator.RunningTasks != 3 {
		t.Errorf("running_tasks = %d, want 3", resp.Data.Orchestrator.RunningTasks)
	}
	if resp.Data.Pool.AcquiredConns != 2 {
		t.Errorf("acquired_conns = %d, want 2", resp.Data.Pool.AcquiredConns)
	}
	if resp.Data.Pool.MaxConns != 10 {
		t.Errorf("max_conns = %d, want 10", resp.Data.Pool.MaxConns)
	}
	if resp.Data.UptimeSeconds < 10 {
		t.Errorf("uptime_seconds = %f, want >= 10", resp.Data.UptimeSeconds)
	}
	if resp.Data.PID == 0 {
		t.Error("pid = 0, want non-zero")
	}
	if resp.Data.Runtime.SysBytes == 0 {
		t.Error("sys_bytes = 0, want non-zero")
	}
}

func TestStatsHandler_NilPoolStatter(t *testing.T) {
	handler := StatsHandler(func() int { return 0 }, nil, time.Now())

	req := httptest.NewRequest(http.MethodGet, "/debug/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp struct {
		Data struct {
			Pool struct {
				MaxConns int32 `json:"max_conns"`
			} `json:"pool"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Data.Pool.MaxConns != 0 {
		t.Errorf("max_conns = %d, want 0 when no pool statter", resp.Data.Pool.MaxConns)
	}
}
