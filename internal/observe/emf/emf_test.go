package emf_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/observe/emf"
	"github.com/okamyuji/firstline/internal/port"
)

func listen(t *testing.T) (net.Listener, chan string) {
	t.Helper()
	return listenOn(t, "127.0.0.1:0")
}

// listenOn accepts connections on addr and sends each line it reads to the channel.
func listenOn(t *testing.T, addr string) (net.Listener, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	lines := make(chan string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()
		}
	}()
	return ln, lines
}

func recv(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("no line received")
		return ""
	}
}

func TestEmitLeavesOutAbsentSizes(t *testing.T) {
	ln, lines := listen(t)
	if err := emf.New(ln.Addr().String()).Emit(context.Background(), port.HealthSnapshot{QueueCap: 1}); err != nil {
		t.Fatal(err)
	}
	line := recv(t, lines)
	if strings.Contains(line, "wal_bytes") || strings.Contains(line, "db_bytes") || !strings.Contains(line, `"queue_cap":1`) {
		t.Fatalf("line %s; want no size metrics", line)
	}
}

func TestEmitSendsOneEMFLine(t *testing.T) {
	ln, lines := listen(t)
	sink := emf.New(ln.Addr().String())
	snap := port.HealthSnapshot{
		CommitWaitP99Ms: 12.5, SyncItemsPerSecMax5m: 40, QueueLen: 3, QueueCap: 8192,
		QueueUsageAvg5m: 0.25, DroppedTotal: 2, WALBytes: new(int64(1000)), DBBytes: new(int64(5000)),
		LastReconcileAt: time.Unix(1700000000, 0), TLSCertDaysLeft: 30,
	}

	if err := sink.Emit(context.Background(), snap); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		AWS struct {
			Timestamp         int64
			LogGroupName      string
			CloudWatchMetrics []struct {
				Namespace  string
				Dimensions [][]string
				Metrics    []struct{ Name, Unit string }
			}
		} `json:"_aws"`
	}
	line := recv(t, lines)
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(line), &raw)

	if doc.AWS.LogGroupName != "/firstline/metrics" || doc.AWS.Timestamp <= 0 {
		t.Fatalf("bad envelope: %s", line)
	}
	if len(doc.AWS.CloudWatchMetrics) != 1 || doc.AWS.CloudWatchMetrics[0].Namespace != "firstline" {
		t.Fatalf("bad directive: %s", line)
	}
	d := doc.AWS.CloudWatchMetrics[0]
	if len(d.Dimensions) != 1 || len(d.Dimensions[0]) != 0 {
		t.Fatalf("want one empty dimension set: %s", line)
	}
	for _, m := range d.Metrics {
		if _, ok := raw[m.Name]; !ok || m.Unit == "" {
			t.Fatalf("metric %q declared without value or unit: %s", m.Name, line)
		}
	}
	want := map[string]float64{
		"commit_wait_p99_ms": 12.5, "sync_items_per_sec_max_5m": 40, "queue_len": 3, "queue_cap": 8192,
		"queue_usage_avg_5m": 0.25, "dropped_total": 2, "wal_bytes": 1000, "db_bytes": 5000,
		"last_reconcile_at": 1700000000, "tls_cert_days_left": 30,
	}
	if len(d.Metrics) != len(want) {
		t.Fatalf("want %d metrics, got %d", len(want), len(d.Metrics))
	}
	for k, v := range want {
		if raw[k] != v {
			t.Fatalf("%s = %v, want %v", k, raw[k], v)
		}
	}
}

func TestEmitZeroReconcileIsZero(t *testing.T) {
	ln, lines := listen(t)
	if err := emf.New(ln.Addr().String()).Emit(context.Background(), port.HealthSnapshot{}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(recv(t, lines)), &raw)
	if raw["last_reconcile_at"] != float64(0) {
		t.Fatalf("got %v", raw["last_reconcile_at"])
	}
}

func TestEmitErrorsWithoutListenerThenReconnects(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	sink := emf.New(addr)

	if err := sink.Emit(context.Background(), port.HealthSnapshot{}); err == nil {
		t.Fatal("want error when nobody listens")
	}

	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("port reused: %v", err)
	}
	t.Cleanup(func() { _ = ln2.Close() })
	got := make(chan string, 1)
	go func() {
		c, err := ln2.Accept()
		if err != nil {
			return
		}
		sc := bufio.NewScanner(c)
		if sc.Scan() {
			got <- sc.Text()
		}
	}()
	if err := sink.Emit(context.Background(), port.HealthSnapshot{}); err != nil {
		t.Fatalf("want reconnect, got %v", err)
	}
	recv(t, got)
}

func TestEmitReportsWriteFailureOnDeadConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()
	sink := emf.New(ln.Addr().String())

	// The first write after the peer closed can still reach the kernel buffer;
	// the reset it provokes fails a later one.
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
}
