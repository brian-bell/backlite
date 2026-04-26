package debug

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/brian-bell/backlite/internal/backup"
	"github.com/brian-bell/backlite/internal/store"
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
	Backup        *backupStats      `json:"backup,omitempty"`
}

type backupStats struct {
	Enabled          bool                `json:"enabled"`
	Directory        string              `json:"directory"`
	IntervalSeconds  float64             `json:"interval_seconds"`
	RetentionSeconds float64             `json:"retention_seconds"`
	WorkerState      string              `json:"worker_state"`
	LatestArtifact   *backup.Metadata    `json:"latest_artifact,omitempty"`
	LastSuccessAt    *time.Time          `json:"last_success_at,omitempty"`
	LastErrorAt      *time.Time          `json:"last_error_at,omitempty"`
	LastErrorMessage string              `json:"last_error_message,omitempty"`
	RecentErrors     []backup.ErrorEntry `json:"recent_errors"`
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
// backupStatusFn may be nil if local backups are not configured;
// when nil or returning a zero (Directory == "" && Enabled == false) Status,
// the response omits the "backup" field.
func StatsHandler(runningFn func() int, ps PoolStatter, startedAt time.Time, backupStatusFn func() backup.Status) http.Handler {
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
		if backupStatusFn != nil {
			s := backupStatusFn()
			if s.Enabled || s.Directory != "" {
				bs := &backupStats{
					Enabled:          s.Enabled,
					Directory:        s.Directory,
					IntervalSeconds:  s.Interval.Seconds(),
					RetentionSeconds: s.Retention.Seconds(),
					WorkerState:      s.WorkerState,
					LatestArtifact:   s.LatestArtifact,
					LastSuccessAt:    s.LastSuccessAt,
					LastErrorAt:      s.LastErrorAt,
					LastErrorMessage: s.LastErrorMessage,
					RecentErrors:     s.RecentErrors,
				}
				if bs.RecentErrors == nil {
					bs.RecentErrors = []backup.ErrorEntry{}
				}
				resp.Backup = bs
			}
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
