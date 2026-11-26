package observe

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store/writer"
)

const walWarnBytes = 1 << 30

// Snapshot gathers the /healthz values at now.
func Snapshot(r *Recorder, w *writer.Writer, dbPath string, lastReconcile, certNotAfter func() time.Time, now time.Time) port.HealthSnapshot {
	st := w.Stats()
	return port.HealthSnapshot{
		CommitWaitP99Ms:      r.CommitWaitP99Ms(now),
		SyncItemsPerSecMax5m: r.SyncItemsPerSecMax(now),
		QueueLen:             st.QueueLen,
		QueueCap:             st.QueueCap,
		QueueUsageAvg5m:      r.QueueUsageAvg(now),
		DroppedTotal:         st.DroppedTotal,
		WALBytes:             fileSize(dbPath + "-wal"),
		DBBytes:              fileSize(dbPath),
		LastReconcileAt:      lastReconcile(),
		TLSCertDaysLeft:      int(math.Floor(certNotAfter().Sub(now).Hours() / 24)),
	}
}

// fileSize is 0 for a missing file: SQLite removes the WAL when it is empty after a checkpoint.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

type healthJSON struct {
	CommitWaitP99Ms      float64    `json:"commit_wait_p99_ms"`
	SyncItemsPerSecMax5m int        `json:"sync_items_per_sec_max_5m"`
	QueueLen             int        `json:"queue_len"`
	QueueCap             int        `json:"queue_cap"`
	QueueUsageAvg5m      float64    `json:"queue_usage_avg_5m"`
	DroppedTotal         int64      `json:"dropped_total"`
	WALBytes             int64      `json:"wal_bytes"`
	DBBytes              int64      `json:"db_bytes"`
	LastReconcileAt      *time.Time `json:"last_reconcile_at"`
	TLSCertDaysLeft      int        `json:"tls_cert_days_left"`
}

// Handler serves the /healthz JSON.
func Handler(snap func() port.HealthSnapshot) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s := snap()
		if s.WALBytes > walWarnBytes {
			slog.WarnContext(req.Context(), "WAL file is over 1 GiB", "wal_bytes", s.WALBytes)
		}
		out := healthJSON{
			s.CommitWaitP99Ms, s.SyncItemsPerSecMax5m, s.QueueLen, s.QueueCap, s.QueueUsageAvg5m,
			s.DroppedTotal, s.WALBytes, s.DBBytes, nil, s.TLSCertDaysLeft,
		}
		if !s.LastReconcileAt.IsZero() {
			t := s.LastReconcileAt.UTC()
			out.LastReconcileAt = &t
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}

var emitFailures atomic.Int64

// EmitFailures counts the Emit errors Run has seen.
func EmitFailures() int64 { return emitFailures.Load() }

// Run emits a snapshot to sink every `every` until ctx ends. A failed Emit is
// logged and counted, and never stops the loop.
func Run(ctx context.Context, after func(time.Duration) <-chan time.Time, every time.Duration,
	snap func() port.HealthSnapshot, sink port.MetricsSink, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-after(every):
		}
		if err := sink.Emit(ctx, snap()); err != nil {
			emitFailures.Add(1)
			log.WarnContext(ctx, "metrics emit failed", "error", err)
		}
	}
}
