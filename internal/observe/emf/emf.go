// Package emf sends health metrics to the CloudWatch Agent in the embedded
// metric format over TCP.
package emf

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/okamyuji/firstline/internal/port"
)

const (
	namespace    = "firstline"
	logGroupName = "/firstline/metrics"
	ioTimeout    = 3 * time.Second
)

type metric struct {
	Name string `json:"Name"`
	Unit string `json:"Unit"`
}

type directive struct {
	Namespace  string     `json:"Namespace"`
	Dimensions [][]string `json:"Dimensions"`
	Metrics    []metric   `json:"Metrics"`
}

type sink struct {
	addr string
	now  func() time.Time

	mu   sync.Mutex // one writer at a time, and guards conn
	conn net.Conn
}

// New returns a MetricsSink that writes one EMF line per Emit to addr. It
// dials lazily and redials after a failed write.
func New(addr string) port.MetricsSink { return &sink{addr: addr, now: time.Now} }

func (s *sink) Emit(ctx context.Context, h port.HealthSnapshot) error {
	var reconcile int64
	if !h.LastReconcileAt.IsZero() {
		reconcile = h.LastReconcileAt.Unix()
	}
	// Order is the order of the Metrics array.
	type val struct {
		name, unit string
		v          any
	}
	vals := []val{
		{"commit_wait_p99_ms", "Milliseconds", h.CommitWaitP99Ms},
		{"sync_items_per_sec_max_5m", "Count", h.SyncItemsPerSecMax5m},
		{"queue_len", "Count", h.QueueLen},
		{"queue_cap", "Count", h.QueueCap},
		{"queue_usage_avg_5m", "None", h.QueueUsageAvg5m},
		{"dropped_total", "Count", h.DroppedTotal},
		{"last_reconcile_at", "Seconds", reconcile},
		{"tls_cert_days_left", "Count", h.TLSCertDaysLeft},
	}
	// A store without a SQLite file has no sizes; a 0 would read as an empty file.
	if h.WALBytes != nil && h.DBBytes != nil {
		vals = append(vals, val{"wal_bytes", "Bytes", *h.WALBytes}, val{"db_bytes", "Bytes", *h.DBBytes})
	}
	doc := make(map[string]any, len(vals))
	ms := make([]metric, len(vals))
	for i, v := range vals {
		doc[v.name] = v.v
		ms[i] = metric{v.name, v.unit}
	}
	doc["_aws"] = map[string]any{
		"Timestamp":    s.now().UnixMilli(),
		"LogGroupName": logGroupName,
		// One empty dimension set: the metrics have no dimensions.
		"CloudWatchMetrics": []directive{{Namespace: namespace, Dimensions: [][]string{{}}, Metrics: ms}},
	}
	line, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return s.write(ctx, append(line, '\n'))
}

func (s *sink) write(ctx context.Context, line []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		d := net.Dialer{Timeout: ioTimeout}
		c, err := d.DialContext(ctx, "tcp", s.addr)
		if err != nil {
			return err
		}
		s.conn = c
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(ioTimeout))
	if _, err := s.conn.Write(line); err != nil {
		_ = s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}
