package emf_test

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/observe/emf"
	"github.com/okamyuji/firstline/internal/port"
)

func TestEmitStampsMillisecondsAndDeclaresEachUnit(t *testing.T) {
	ln, lines := listen(t)
	snap := port.HealthSnapshot{WALBytes: new(int64(1)), DBBytes: new(int64(2))}
	if err := emf.New(ln.Addr().String()).Emit(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		AWS struct {
			Timestamp         int64
			CloudWatchMetrics []struct{ Metrics []struct{ Name, Unit string } }
		} `json:"_aws"`
	}
	if err := json.Unmarshal([]byte(recv(t, lines)), &doc); err != nil {
		t.Fatal(err)
	}
	// CloudWatch reads Timestamp as milliseconds since the epoch.
	if d := time.Now().UnixMilli() - doc.AWS.Timestamp; d < 0 || d > 60_000 {
		t.Errorf("Timestamp %d is not within 60s of now in milliseconds", doc.AWS.Timestamp)
	}
	units := map[string]string{}
	for _, m := range doc.AWS.CloudWatchMetrics[0].Metrics {
		units[m.Name] = m.Unit
	}
	want := map[string]string{
		"commit_wait_p99_ms": "Milliseconds", "sync_items_per_sec_max_5m": "Count", "queue_len": "Count",
		"queue_cap": "Count", "queue_usage_avg_5m": "None", "dropped_total": "Count", "wal_bytes": "Bytes",
		"db_bytes": "Bytes", "last_reconcile_at": "Seconds", "tls_cert_days_left": "Count",
	}
	if len(units) != len(want) {
		t.Errorf("units = %v; want %v", units, want)
	}
	for name, unit := range want {
		if units[name] != unit {
			t.Errorf("unit of %s = %q; want %q", name, units[name], unit)
		}
	}
}

func TestEmitRedialsAfterAFailedWrite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	sink := emf.New(addr)
	var failed error
	for range 100 {
		if failed = sink.Emit(context.Background(), port.HealthSnapshot{}); failed != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if failed == nil {
		t.Fatal("want a write error once the peer is gone")
	}
	_ = ln.Close()
	ln2, lines := listenOn(t, addr)
	defer func() { _ = ln2.Close() }()
	if err := sink.Emit(context.Background(), port.HealthSnapshot{}); err != nil {
		t.Fatalf("Emit after the agent came back = %v", err)
	}
	recv(t, lines)
}

// A peer that accepts and never reads fills the socket buffers; the write
// deadline must turn the stuck write into an error.
func TestEmitGivesUpOnAPeerThatStopsReading(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	held := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			held <- c
		}
	}()
	t.Cleanup(func() {
		select {
		case c := <-held:
			_ = c.Close()
		default:
		}
	})
	sink := emf.New(ln.Addr().String())
	done := make(chan error, 1)
	go func() {
		for {
			if err := sink.Emit(context.Background(), port.HealthSnapshot{}); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		t.Logf("Emit gave up: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Emit still blocked 20s after the peer stopped reading")
	}
}
