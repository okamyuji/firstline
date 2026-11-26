package observe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/okamyuji/firstline/internal/observe"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store/writer"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestP99ExcludesWaitsOlderThanFiveMinutes(t *testing.T) {
	r := observe.NewRecorder()
	r.OnSyncResult(t0, ms(900))
	for i := range 100 {
		r.OnSyncResult(t0.Add(6*time.Minute).Add(time.Duration(i)*time.Millisecond), ms(10))
	}

	got := r.CommitWaitP99Ms(t0.Add(6*time.Minute + time.Second))

	if got != 10 {
		t.Fatalf("p99 = %v, want 10 (old 900ms sample must be out)", got)
	}
}

func TestP99IsNearestRank(t *testing.T) {
	r := observe.NewRecorder()
	for i := 1; i <= 100; i++ {
		r.OnSyncResult(t0, ms(i))
	}
	if got := r.CommitWaitP99Ms(t0); got != 99 {
		t.Fatalf("p99 = %v, want 99", got)
	}
	if got := observe.NewRecorder().CommitWaitP99Ms(t0); got != 0 {
		t.Fatalf("empty p99 = %v, want 0", got)
	}
}

func TestP99WindowBoundary(t *testing.T) {
	r := observe.NewRecorder()
	r.OnSyncResult(t0, ms(500))
	if got := r.CommitWaitP99Ms(t0.Add(5*time.Minute - time.Millisecond)); got != 500 {
		t.Fatalf("just inside window: %v", got)
	}
	if got := r.CommitWaitP99Ms(t0.Add(5 * time.Minute)); got != 0 {
		t.Fatalf("at 5 min the sample must be out: %v", got)
	}
}

func TestSyncMaxPerSecondExcludesOldSeconds(t *testing.T) {
	r := observe.NewRecorder()
	for range 50 {
		r.OnSyncResult(t0, ms(1))
	}
	for range 7 {
		r.OnSyncResult(t0.Add(6*time.Minute), ms(1))
	}
	for range 3 {
		r.OnSyncResult(t0.Add(6*time.Minute+time.Second), ms(1))
	}

	if got := r.SyncItemsPerSecMax(t0.Add(6*time.Minute + time.Second)); got != 7 {
		t.Fatalf("max = %d, want 7", got)
	}
	if got := r.SyncItemsPerSecMax(t0); got != 50 {
		t.Fatalf("max = %d, want 50", got)
	}
	if got := r.SyncItemsPerSecMax(t0.Add(5 * time.Minute)); got != 0 {
		t.Fatalf("second 300 s old must be out: %d", got)
	}
	if got := r.SyncItemsPerSecMax(t0.Add(5*time.Minute - time.Second)); got != 50 {
		t.Fatalf("second 299 s old must be in: %d", got)
	}
}

func TestQueueUsageAverage(t *testing.T) {
	r := observe.NewRecorder()
	r.SampleQueue(t0, 0, 100)
	r.SampleQueue(t0.Add(time.Second), 50, 100)
	r.SampleQueue(t0.Add(2*time.Second), 100, 100)
	if got := r.QueueUsageAvg(t0.Add(2 * time.Second)); got != 0.5 {
		t.Fatalf("avg = %v, want 0.5", got)
	}
	if got := r.QueueUsageAvg(t0.Add(300 * time.Second)); got != 0.75 {
		t.Fatalf("t0 sample out, 1s and 2s in: avg = %v, want 0.75", got)
	}
	if got := observe.NewRecorder().QueueUsageAvg(t0); got != 0 {
		t.Fatalf("empty avg = %v", got)
	}
}

func TestQueueUsageIgnoresZeroCap(t *testing.T) {
	r := observe.NewRecorder()
	r.SampleQueue(t0, 5, 0)
	if got := r.QueueUsageAvg(t0); got != 0 {
		t.Fatalf("avg = %v", got)
	}
}

func TestRecorderMemoryIsBounded(t *testing.T) {
	r := observe.NewRecorder()
	for i := range 200_000 {
		r.OnSyncResult(t0.Add(time.Duration(i)*time.Microsecond), ms(1))
	}
	if n := r.SampleCount(); n != 1<<16 {
		t.Fatalf("wait samples = %d, want the ring size %d", n, 1<<16)
	}
}

func TestRecorderIsSafeForConcurrentUse(t *testing.T) {
	r := observe.NewRecorder()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range 1000 {
				at := t0.Add(time.Duration(i) * time.Second)
				r.OnSyncResult(at, ms(i%50))
				r.SampleQueue(at, i%10, 10)
				r.CommitWaitP99Ms(at)
				r.SyncItemsPerSecMax(at)
				r.QueueUsageAvg(at)
			}
		})
	}
	wg.Wait()
}

func newWriter(t *testing.T, r *observe.Recorder) *writer.Writer {
	t.Helper()
	w, err := writer.New((*bun.DB)(nil), writer.Options{
		QueueCap: 16, BatchMax: 4, BatchWait: ms(1),
		IsIOError: func(error) bool { return false }, OnFatal: func(error) {},
		Now: func() time.Time { return t0 }, After: func(time.Duration) <-chan time.Time { return nil },
		OnSyncResult: r.OnSyncResult, OnAsyncError: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestSnapshotCollectsEveryField(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "f.db")
	if err := os.WriteFile(db, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db+"-wal", make([]byte, 123), 0o600); err != nil {
		t.Fatal(err)
	}
	r := observe.NewRecorder()
	r.OnSyncResult(t0, ms(8))
	r.SampleQueue(t0, 4, 16)
	rec := t0.Add(-time.Hour)

	s := observe.Snapshot(r, newWriter(t, r), db,
		func() time.Time { return rec },
		func() time.Time { return t0.Add(14*24*time.Hour + 5*time.Hour) }, t0)

	want := port.HealthSnapshot{
		CommitWaitP99Ms: 8, SyncItemsPerSecMax5m: 1, QueueLen: 0, QueueCap: 16,
		QueueUsageAvg5m: 0.25, WALBytes: new(int64(123)), DBBytes: new(int64(4096)), LastReconcileAt: rec, TLSCertDaysLeft: 14,
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("got %+v\nwant %+v", s, want)
	}
}

func TestSnapshotMissingFilesAreZero(t *testing.T) {
	r := observe.NewRecorder()
	s := observe.Snapshot(r, newWriter(t, r), filepath.Join(t.TempDir(), "none.db"),
		func() time.Time { return time.Time{} }, func() time.Time { return t0.Add(-25 * time.Hour) }, t0)
	if *s.WALBytes != 0 || *s.DBBytes != 0 {
		t.Fatalf("sizes = %d, %d", *s.WALBytes, *s.DBBytes)
	}
	if s.TLSCertDaysLeft != -2 {
		t.Fatalf("expired cert days = %d, want -2 (floor)", s.TLSCertDaysLeft)
	}
}

func TestHandlerReturnsTheSpecFields(t *testing.T) {
	rec := t0.Add(-time.Minute)
	h := observe.Handler(discard, func() port.HealthSnapshot {
		return port.HealthSnapshot{CommitWaitP99Ms: 1.5, SyncItemsPerSecMax5m: 2, QueueLen: 3, QueueCap: 4,
			QueueUsageAvg5m: 0.5, DroppedTotal: 6, WALBytes: new(int64(7)), DBBytes: new(int64(8)), LastReconcileAt: rec, TLSCertDaysLeft: 9}
	})
	w := httptest.NewRecorder()

	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	wantKeys := []string{"commit_wait_p99_ms", "db_bytes", "dropped_total", "last_reconcile_at", "queue_cap",
		"queue_len", "queue_usage_avg_5m", "sync_items_per_sec_max_5m", "tls_cert_days_left", "wal_bytes"}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("keys %v", keys)
	}
	if got["commit_wait_p99_ms"] != 1.5 || got["queue_len"] != 3.0 || got["queue_cap"] != 4.0 ||
		got["tls_cert_days_left"] != 9.0 || got["wal_bytes"] != 7.0 || got["db_bytes"] != 8.0 || got["last_reconcile_at"] != "2026-11-26T11:59:00Z" {
		t.Fatalf("values %v", got)
	}
}

func TestSnapshotCountsDroppedAsyncItems(t *testing.T) {
	r := observe.NewRecorder()
	w := newWriter(t, r)
	// The writer is not running, so the item after the 16 queued ones is dropped.
	for range 17 {
		w.Send(func(context.Context, bun.Tx) error { return nil })
	}
	s := observe.Snapshot(r, w, "", func() time.Time { return time.Time{} }, func() time.Time { return t0 }, t0)
	if s.DroppedTotal != 1 || s.QueueLen != 16 {
		t.Errorf("dropped %d, queue %d; want 1 and 16", s.DroppedTotal, s.QueueLen)
	}
}

func TestHandlerWritesReconcileTimeInUTC(t *testing.T) {
	at := time.Date(2026, 11, 26, 21, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	w := httptest.NewRecorder()
	observe.Handler(discard, func() port.HealthSnapshot { return port.HealthSnapshot{LastReconcileAt: at} }).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(w.Body.String(), `"last_reconcile_at":"2026-11-26T12:00:00Z"`) {
		t.Fatalf("body %s", w.Body)
	}
}

func TestSnapshotWithoutASQLiteFileReportsNoSizes(t *testing.T) {
	r := observe.NewRecorder()
	s := observe.Snapshot(r, newWriter(t, r), "",
		func() time.Time { return time.Time{} }, func() time.Time { return t0 }, t0)
	if s.WALBytes != nil || s.DBBytes != nil {
		t.Fatalf("sizes = %v, %v; want absent", s.WALBytes, s.DBBytes)
	}
	w := httptest.NewRecorder()
	observe.Handler(discard, func() port.HealthSnapshot { return s }).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(w.Body.String(), `"wal_bytes":null,"db_bytes":null`) {
		t.Fatalf("body %s", w.Body)
	}
}

func TestHandlerNullReconcileBeforeFirstRun(t *testing.T) {
	w := httptest.NewRecorder()
	observe.Handler(discard, func() port.HealthSnapshot { return port.HealthSnapshot{} }).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(w.Body.String(), `"last_reconcile_at":null`) {
		t.Fatalf("body %s", w.Body)
	}
}

var discard = slog.New(slog.DiscardHandler)

func TestHandlerWarnsWhenWALExceedsOneGiB(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	get := func(wal int64) {
		observe.Handler(log, func() port.HealthSnapshot { return port.HealthSnapshot{WALBytes: &wal} }).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}

	get(1 << 30)
	if buf.Len() != 0 {
		t.Fatalf("exactly 1 GiB must not warn: %s", buf.String())
	}
	get(1<<30 + 1)
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("want WARN, got %q", buf.String())
	}
}

type sinkFunc func(context.Context, port.HealthSnapshot) error

func (f sinkFunc) Emit(ctx context.Context, s port.HealthSnapshot) error { return f(ctx, s) }

func TestRunEmitsEveryTickAndCountsFailures(t *testing.T) {
	var logs bytes.Buffer
	ticks := make(chan time.Time)
	after := func(time.Duration) <-chan time.Time { return ticks }
	calls := make(chan struct{}, 4)
	var mu sync.Mutex
	n := 0
	sink := sinkFunc(func(context.Context, port.HealthSnapshot) error {
		mu.Lock()
		defer mu.Unlock()
		n++
		calls <- struct{}{}
		if n == 1 {
			return errors.New("down")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	before := observe.EmitFailures()
	done := make(chan struct{})
	go func() {
		observe.Run(ctx, after, time.Minute, func() port.HealthSnapshot { return port.HealthSnapshot{} }, sink,
			slog.New(slog.NewJSONHandler(&logs, nil)))
		close(done)
	}()

	ticks <- t0
	<-calls
	ticks <- t0
	<-calls
	cancel()
	<-done

	if observe.EmitFailures()-before != 1 {
		t.Fatalf("failures = %d", observe.EmitFailures()-before)
	}
	if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), "down") {
		t.Fatalf("log %q", logs.String())
	}
}
