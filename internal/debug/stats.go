package debug

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/backflow-labs/backflow/internal/store"
)

// PoolStatter is implemented by store backends that can report connection pool
// statistics. The debug handler type-asserts to this at call time.
type PoolStatter interface {
	PoolStats() store.PoolStats
}

type statsResponse struct {
	Orchestrator  orchestratorStats `json:"orchestrator"`
	Pool          poolStats         `json:"pool"`
	UptimeSeconds float64           `json:"uptime_seconds"`
	Runtime       runtimeStats      `json:"runtime"`
	PID           int               `json:"pid"`
}

type orchestratorStats struct {
	RunningTasks int `json:"running_tasks"`
}

type poolStats struct {
	AcquiredConns int32 `json:"acquired_conns"`
	IdleConns     int32 `json:"idle_conns"`
	TotalConns    int32 `json:"total_conns"`
	MaxConns      int32 `json:"max_conns"`
}

type runtimeStats struct {
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`
	SysBytes       uint64 `json:"sys_bytes"`
}

type envelope struct {
	Data any `json:"data"`
}

// StatsHandler returns an http.Handler that serves /debug/stats.
// runningFn returns the orchestrator's current running task count.
// ps may be nil if the store does not support pool stats.
func StatsHandler(runningFn func() int, ps PoolStatter, startedAt time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var pool poolStats
		if ps != nil {
			s := ps.PoolStats()
			pool = poolStats{
				AcquiredConns: s.AcquiredConns,
				IdleConns:     s.IdleConns,
				TotalConns:    s.TotalConns,
				MaxConns:      s.MaxConns,
			}
		}

		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)

		resp := statsResponse{
			Orchestrator:  orchestratorStats{RunningTasks: runningFn()},
			Pool:          pool,
			UptimeSeconds: time.Since(startedAt).Seconds(),
			Runtime: runtimeStats{
				HeapAllocBytes: mem.HeapAlloc,
				SysBytes:       mem.Sys,
			},
			PID: os.Getpid(),
		}

		data, err := json.Marshal(envelope{Data: resp})
		if err != nil {
			log.Error().Err(err).Msg("failed to marshal debug stats")
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})
}
