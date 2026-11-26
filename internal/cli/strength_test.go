package cli

import (
	"bufio"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/auth"
	"github.com/okamyuji/firstline/internal/store"
)

var discardLog = slog.New(slog.DiscardHandler)

// onSale opens a wiring with the overrides and creates one event on sale now.
func onSale(t *testing.T, over map[string]string) (*wiring, string) {
	t.Helper()
	env, _ := serveEnv(t)
	env["FIRSTLINE_METRICS"] = "none"
	for k, v := range over {
		env[k] = v
	}
	cfg, err := LoadConfig(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := loadCert(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		t.Fatal(err)
	}
	wr := newWiring(cfg, cs, &recordingReporter{}, discardLog)
	if err := wr.open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wr.pay.Stop(); wr.w.Stop(); _ = closeStore(wr.st) })
	now := time.Now()
	id, err := wr.svc.CreateEvent(t.Context(), "org", app.EventInput{
		TitleJa: "題", TitleEn: "t", StartsAt: now.Add(24 * time.Hour), EndsAt: now.Add(26 * time.Hour),
		SaleStartsAt: now.Add(-time.Hour), Capacity: 10, Price: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	return wr, id
}

func buy(wr *wiring, eventID, email string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/events/"+eventID+"/purchases",
		strings.NewReader(url.Values{"email": {email}, "quantity": {"1"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "10.0.0.9:1234"
	rec := httptest.NewRecorder()
	wr.https.Handler.ServeHTTP(rec, r)
	return rec
}

func TestServeWiresTheHoldRateLimit(t *testing.T) {
	wr, id := onSale(t, map[string]string{"FIRSTLINE_HOLD_RATE_PER_MIN": "1"})
	if rec := buy(wr, id, "a@example.com"); rec.Code != http.StatusSeeOther {
		t.Fatalf("first hold = %d", rec.Code)
	}
	if rec := buy(wr, id, "b@example.com"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("second hold from the same IP = %d; want 429", rec.Code)
	}
}

func TestServeWiresTheTokenKeyAndSyncMetrics(t *testing.T) {
	wr, id := onSale(t, nil)
	loc, err := url.Parse(buy(wr, id, "a@example.com").Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	purchase := strings.TrimPrefix(loc.Path, "/purchases/")
	status := func(tok string) int {
		rec := httptest.NewRecorder()
		wr.https.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, loc.Path+"?token="+url.QueryEscape(tok), nil))
		return rec.Code
	}
	if got := status(loc.Query().Get("token")); got != http.StatusOK {
		t.Fatalf("status with the issued token = %d", got)
	}
	forged := auth.Tokens{}.Issue(purchase, time.Now().Add(time.Hour))
	if got := status(forged); got != http.StatusNotFound {
		t.Errorf("status with a token signed by an empty key = %d; want 404", got)
	}
	if got := wr.snapshot().SyncItemsPerSecMax5m; got < 1 {
		t.Errorf("sync_items_per_sec_max_5m = %d after a hold; want at least 1", got)
	}
}

// blockWriter holds the writer goroutine in one commit until the test ends,
// so items sent after it stay in the queue.
func blockWriter(t *testing.T, wr *wiring) {
	t.Helper()
	inside, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	go func() {
		_ = wr.w.Do(context.Background(), func(context.Context, bun.Tx) error { close(inside); <-release; return nil })
	}()
	<-inside
}

func TestServeSamplesTheQueue(t *testing.T) {
	wr, _ := onSale(t, nil)
	blockWriter(t, wr)
	for range 8 {
		wr.w.Send(func(context.Context, bun.Tx) error { return nil })
	}
	for _, j := range wr.lifecycle().jobs() {
		if j.name == "sample queue" {
			j.run(t.Context())
		}
	}
	if got := wr.rec.QueueUsageAvg(time.Now()); got <= 0 {
		t.Errorf("queue_usage_avg_5m = %v with 8 queued items; want above 0", got)
	}
}

func TestServeSendsEMFToTheConfiguredAddressEveryMinute(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	lines := make(chan string, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			if sc := bufio.NewScanner(c); sc.Scan() {
				lines <- sc.Text()
			}
			_ = c.Close()
		}
	}()
	wr, _ := onSale(t, map[string]string{"FIRSTLINE_METRICS": "emf", "FIRSTLINE_EMF_ADDR": ln.Addr().String()})
	l := wr.lifecycle()
	var mu sync.Mutex
	var every []time.Duration
	l.after = func(d time.Duration) <-chan time.Time {
		mu.Lock()
		every = append(every, d)
		mu.Unlock()
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { l.loops[0](ctx); close(done) }()
	select {
	case line := <-lines:
		if !strings.Contains(line, `"_aws"`) {
			t.Errorf("line %q is not EMF", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no EMF line at FIRSTLINE_EMF_ADDR")
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(every) == 0 || every[0] != time.Minute {
		t.Errorf("EMF intervals %v; want 1m", every)
	}
}

// The request deadline is observable only by waiting for it, so this test
// takes requestDeadline: a login waiting for a free Argon2id slot must end.
func TestServeGivesEveryRequestTheDeadline(t *testing.T) {
	t.Parallel()
	wr, _ := onSale(t, nil)
	for range runtime.NumCPU() {
		if err := wr.svc.Limiter.Acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	answered := make(chan int, 1)
	start := time.Now()
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/admin/login",
			strings.NewReader(url.Values{"email": {"a@example.com"}, "password": {"long enough pw"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		wr.https.Handler.ServeHTTP(rec, r)
		answered <- rec.Code
	}()
	select {
	case code := <-answered:
		if took := time.Since(start); code != http.StatusServiceUnavailable || took < requestDeadline-time.Second {
			t.Errorf("answered %d after %v; want 503 at the %v deadline", code, took, requestDeadline)
		}
	case <-time.After(requestDeadline + 5*time.Second):
		t.Fatal("a login waiting for a slot never ended")
	}
}

// barrierServer's Shutdown returns only once every server has entered
// Shutdown, so serial calls run into the timeout.
type barrierServer struct {
	entered *sync.WaitGroup
}

func (b barrierServer) Shutdown(context.Context) error {
	b.entered.Done()
	done := make(chan struct{})
	go func() { b.entered.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("shut down one at a time")
	}
}

func TestShutdownCallsTheServersInParallel(t *testing.T) {
	var entered sync.WaitGroup
	entered.Add(3)
	var errs []string
	l := &lifecycle{check: func(_ context.Context, what string, err error) {
		if err != nil {
			errs = append(errs, what)
		}
	}}
	for _, name := range []string{"admin", "admin socket", "https"} {
		l.listeners = append(l.listeners, listener{name: name, srv: barrierServer{&entered}})
	}
	l.shutdown()
	if len(errs) != 0 {
		t.Errorf("shutdown errors %v; want the three in parallel", errs)
	}
}

func TestServeListensInSpecOrder(t *testing.T) {
	wr, _ := onSale(t, nil)
	var names []string
	for _, s := range wr.lifecycle().listeners {
		names = append(names, s.name)
	}
	if !slices.Equal(names, []string{"admin", "admin socket", "https"}) {
		t.Errorf("listeners %v", names)
	}
}

// closeConnector records when its DB closes: sql.DB.Close calls Close on a
// connector that has one.
type closeConnector struct {
	name   string
	closed *[]string
}

func (c closeConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("not connectable")
}
func (c closeConnector) Driver() driver.Driver { return nil }
func (c closeConnector) Close() error {
	*c.closed = append(*c.closed, c.name)
	return nil
}

func TestCloseStoreClosesReadBeforeWrite(t *testing.T) {
	var closed []string
	db := func(name string) *bun.DB {
		return bun.NewDB(sql.OpenDB(closeConnector{name, &closed}), sqlitedialect.New())
	}
	if err := closeStore(&store.Store{Read: db("read"), Write: db("write")}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(closed, []string{"read", "write"}) {
		t.Errorf("closed %v; want read, then write", closed)
	}
}

func TestDirectCloseCommitsQueuedItems(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(envOf(map[string]string{"FIRSTLINE_DB_PATH": filepath.Join(dir, "db"), "FIRSTLINE_LOCK_PATH": filepath.Join(dir, "l")}))
	if err != nil {
		t.Fatal(err)
	}
	d, err := openDirect(t.Context(), cfg, nil, &recordingReporter{}, discardLog)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		d.w.Send(func(ctx context.Context, tx bun.Tx) error {
			_, err := d.st.Exec(ctx, tx, "insert_access_log", "GET", "/", 200, 1, 1)
			return err
		})
	}
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	st, err := openStore(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore(st) }()
	var n int
	if err := st.Read.QueryRowContext(t.Context(), "SELECT count(*) FROM access_logs").Scan(&n); err != nil || n != 50 {
		t.Errorf("access logs after close = %d (%v); want 50", n, err)
	}
}

func TestLockFileIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l")
	f, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode %v (%v); want 0600", fi.Mode().Perm(), err)
	}
}
