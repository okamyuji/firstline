package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type recordingReporter struct {
	mu   sync.Mutex
	errs []error
}

func (r *recordingReporter) Report(_ context.Context, err error, _ ...slog.Attr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
}

// recorder keeps the order in which the fake parts were called.
type recorder struct {
	mu    sync.Mutex
	steps []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, s)
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.steps)
}

type fakeServer struct {
	name     string
	rec      *recorder
	mu       sync.Mutex
	deadline time.Time
}

func (s *fakeServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline, _ = ctx.Deadline()
	s.rec.add("shutdown " + s.name)
	return nil
}

var fixedNow = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)

type fakeParts struct {
	rec       *recorder
	servers   []*fakeServer
	since     []time.Time
	intervals []time.Duration
	errc      chan<- error
}

// newFakeLifecycle returns a lifecycle whose every part records its call.
// after never fires, so the periodic jobs start but never run.
func newFakeLifecycle() (*lifecycle, *fakeParts) {
	p := &fakeParts{rec: &recorder{}}
	var mu sync.Mutex
	step := func(name string) func(context.Context) error {
		return func(context.Context) error { p.rec.add(name); return nil }
	}
	l := &lifecycle{
		now: func() time.Time { return fixedNow },
		after: func(d time.Duration) <-chan time.Time {
			mu.Lock()
			p.intervals = append(p.intervals, d)
			mu.Unlock()
			return nil
		},
		check: func(_ context.Context, what string, err error) {
			if err != nil {
				p.rec.add("check " + what)
			}
		},
		open:    step("open store and start writer"),
		rebuild: step("rebuild inventory"),
		reconcile: func(_ context.Context, since time.Time) error {
			p.since = append(p.since, since)
			p.rec.add("reconcile")
			return nil
		},
		expire:    step("expire"),
		refunds:   step("refunds"),
		mail:      step("mail"),
		purge:     func(context.Context) { p.rec.add("purge sessions") },
		purgeLogs: func(context.Context) { p.rec.add("purge access logs") },
		sweep:     func(context.Context) { p.rec.add("sweep throttle") },
		sample:    func(context.Context) { p.rec.add("sample queue") },
		loops: []func(context.Context){func(ctx context.Context) {
			<-ctx.Done()
			p.rec.add("background stopped")
		}},
		stopPayment: func() { p.rec.add("stop payment") },
		stopWriter:  func() { p.rec.add("stop writer") },
		closeStore:  func() error { p.rec.add("close store"); return nil },
	}
	for _, name := range []string{"admin", "admin socket", "https"} {
		s := &fakeServer{name: name, rec: p.rec}
		p.servers = append(p.servers, s)
		l.listeners = append(l.listeners, listener{name: name, srv: s, start: func(errc chan<- error) error {
			p.errc = errc
			p.rec.add("start " + name)
			return nil
		}})
	}
	return l, p
}

func runAsync(l *lifecycle, term <-chan os.Signal) <-chan error {
	out := make(chan error, 1)
	go func() { out <- l.run(term) }()
	return out
}

func waitFor(t *testing.T, rec *recorder, step string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(rec.got(), step) {
		if time.Now().After(deadline) {
			t.Fatalf("step %q never ran; steps: %v", step, rec.got())
		}
		time.Sleep(time.Millisecond)
	}
}

// runUntilTERM runs the fake lifecycle, checks it waits for SIGTERM, then stops it.
func runUntilTERM(t *testing.T) *fakeParts {
	t.Helper()
	l, p := newFakeLifecycle()
	term := make(chan os.Signal, 1)
	done := runAsync(l, term)
	waitFor(t, p.rec, "start https")
	select {
	case err := <-done:
		t.Fatalf("run returned %v before SIGTERM", err)
	case <-time.After(20 * time.Millisecond):
	}
	term <- syscall.SIGTERM
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return p
}

var startupSteps = []string{"open store and start writer", "rebuild inventory", "reconcile", "expire", "refunds",
	"purge access logs", "start admin", "start admin socket", "start https"}

func TestStartupRunsInSpecOrder(t *testing.T) {
	p := runUntilTERM(t)
	if got := p.rec.got()[:len(startupSteps)]; !slices.Equal(got, startupSteps) {
		t.Errorf("startup steps = %v; want %v", got, startupSteps)
	}
	if want := fixedNow.Add(-24 * time.Hour); len(p.since) != 1 || !p.since[0].Equal(want) {
		t.Errorf("startup reconcile since = %v; want [%v]", p.since, want)
	}
	if got := slices.Sorted(slices.Values(p.intervals)); !slices.Equal(got, []time.Duration{
		time.Second, time.Second, 5 * time.Second, 30 * time.Second, 30 * time.Second, time.Minute, time.Hour, 24 * time.Hour}) {
		t.Errorf("job intervals = %v", got)
	}
}

func TestShutdownRunsInSpecOrder(t *testing.T) {
	p := runUntilTERM(t)
	steps := p.rec.got()[len(startupSteps):]
	shutdowns := slices.Sorted(slices.Values(steps[:3]))
	if !slices.Equal(shutdowns, []string{"shutdown admin", "shutdown admin socket", "shutdown https"}) {
		t.Errorf("shutdown step 1 = %v; want the three Shutdown calls", shutdowns)
	}
	if !slices.Equal(steps[3:], []string{"stop payment", "background stopped", "stop writer", "close store"}) {
		t.Errorf("shutdown steps 2 to 4 = %v", steps[3:])
	}
}

func TestShutdownsShareOneTwentySecondDeadline(t *testing.T) {
	p := runUntilTERM(t)
	d0 := p.servers[0].deadline
	for _, s := range p.servers {
		if s.deadline.IsZero() || !s.deadline.Equal(d0) {
			t.Errorf("Shutdown deadlines = %v, %v; want one shared deadline", s.deadline, d0)
		}
	}
	if left := time.Until(d0); left < 19*time.Second || left > 20*time.Second {
		t.Errorf("Shutdown deadline is %v away; want 20s", left)
	}
}

func TestJobsTable(t *testing.T) {
	l, p := newFakeLifecycle()
	want := map[string]time.Duration{
		"expire holds": time.Second, "retry refunds": 30 * time.Second, "reconcile payments": 30 * time.Second,
		"send mail": 5 * time.Second, "purge sessions": time.Hour, "purge access logs": 24 * time.Hour,
		"sweep throttle": time.Minute, "sample queue": time.Second,
	}
	jobs := l.jobs()
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %d; want %d", len(jobs), len(want))
	}
	for _, j := range jobs {
		if want[j.name] != j.every {
			t.Errorf("job %q every %v; want %v", j.name, j.every, want[j.name])
		}
		j.run(context.Background())
	}
	if got := p.rec.got(); !slices.Equal(got, []string{"expire", "refunds", "reconcile", "mail",
		"purge sessions", "purge access logs", "sweep throttle", "sample queue"}) {
		t.Errorf("jobs ran %v", got)
	}
	if want := fixedNow.Add(-15 * time.Minute); len(p.since) != 1 || !p.since[0].Equal(want) {
		t.Errorf("periodic reconcile since = %v; want [%v]", p.since, want)
	}
}

func TestJobsReportErrors(t *testing.T) {
	l, p := newFakeLifecycle()
	fail := func(context.Context) error { return errors.New("x") }
	l.expire, l.refunds, l.mail = fail, fail, fail
	l.reconcile = func(context.Context, time.Time) error { return errors.New("x") }
	for _, j := range l.jobs() {
		j.run(context.Background())
	}
	if got := p.rec.got(); !slices.Equal(got, []string{"check expire holds", "check retry refunds",
		"check reconcile payments", "check send mail", "purge sessions", "purge access logs", "sweep throttle", "sample queue"}) {
		t.Errorf("steps = %v", got)
	}
}

func TestStartupCatchUpErrorsAreReportedNotFatal(t *testing.T) {
	l, p := newFakeLifecycle()
	l.reconcile = func(context.Context, time.Time) error { return errors.New("x") }
	l.expire = func(context.Context) error { return errors.New("x") }
	l.refunds = func(context.Context) error { return errors.New("x") }
	term := make(chan os.Signal, 1)
	done := runAsync(l, term)
	waitFor(t, p.rec, "start https")
	term <- syscall.SIGTERM
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := p.rec.got()[2:5]; !slices.Equal(got, []string{"check startup reconcile", "check startup expire", "check startup refunds"}) {
		t.Errorf("steps = %v", got)
	}
}

func TestOpenFailureStopsBeforeAnythingElse(t *testing.T) {
	l, p := newFakeLifecycle()
	l.open = func(context.Context) error { return errors.New("disk") }
	if err := l.run(make(chan os.Signal)); err == nil {
		t.Fatal("run succeeded")
	}
	if got := p.rec.got(); len(got) != 0 {
		t.Errorf("steps after failed open = %v; want none", got)
	}
}

func TestRebuildFailureStopsWriterAndStore(t *testing.T) {
	l, p := newFakeLifecycle()
	l.rebuild = func(context.Context) error { return errors.New("bad") }
	if err := l.run(make(chan os.Signal)); err == nil || !strings.Contains(err.Error(), "rebuild inventory") {
		t.Fatalf("run = %v; want a rebuild error", err)
	}
	if got := p.rec.got(); !slices.Equal(got, []string{"open store and start writer", "stop payment", "stop writer", "close store"}) {
		t.Errorf("steps = %v", got)
	}
}

func TestListenFailureShutsDownWhatStarted(t *testing.T) {
	l, p := newFakeLifecycle()
	l.listeners[1].start = func(chan<- error) error { return errors.New("address in use") }
	if err := l.run(make(chan os.Signal)); err == nil {
		t.Fatal("run succeeded")
	}
	got := p.rec.got()
	if slices.Contains(got, "start https") || !slices.Contains(got, "shutdown https") || got[len(got)-1] != "close store" {
		t.Errorf("steps = %v", got)
	}
}

func TestServeErrorTriggersShutdown(t *testing.T) {
	l, p := newFakeLifecycle()
	done := runAsync(l, make(chan os.Signal))
	waitFor(t, p.rec, "start https")
	p.errc <- errors.New("listener died")
	if err := <-done; err == nil || err.Error() != "listener died" {
		t.Fatalf("run = %v; want the serve error", err)
	}
	if got := p.rec.got(); got[len(got)-1] != "close store" {
		t.Errorf("steps = %v", got)
	}
}

func TestCloseStoreErrorIsReported(t *testing.T) {
	l, p := newFakeLifecycle()
	l.closeStore = func() error { return errors.New("x") }
	term := make(chan os.Signal, 1)
	term <- syscall.SIGTERM
	if err := l.run(term); err != nil {
		t.Fatal(err)
	}
	if got := p.rec.got(); got[len(got)-1] != "check close store" {
		t.Errorf("steps = %v", got)
	}
}

func TestStartOnListensBeforeReturningAndReportsServeResult(t *testing.T) {
	addr := freeAddr(t)
	errc := make(chan error, 1)
	served := errors.New("served")
	ready := func() error { return nil }
	if err := startOn("tcp", addr, ready, func(ln net.Listener) error { _ = ln.Close(); return served })(errc); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; !errors.Is(err, served) {
		t.Errorf("errc got %v; want the serve result", err)
	}
	if err := startOn("tcp", "256.0.0.1:1", ready, nil)(errc); err == nil {
		t.Error("startOn on an invalid address succeeded")
	}
}

func TestStartOnClosesTheListenerWhenReadyFails(t *testing.T) {
	addr := freeAddr(t)
	notReady := errors.New("chmod failed")
	err := startOn("tcp", addr, func() error { return notReady }, func(net.Listener) error {
		t.Error("serve ran after ready failed")
		return nil
	})(make(chan error, 1))
	if !errors.Is(err, notReady) {
		t.Fatalf("startOn = %v; want the ready error", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("address still held after a failed start: %v", err)
	}
	_ = ln.Close()
}

func TestHTTPServerTimeouts(t *testing.T) {
	s := newServer(nil, slog.Default())
	if s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 10*time.Second ||
		s.WriteTimeout != 15*time.Second || s.IdleTimeout != 60*time.Second {
		t.Errorf("timeouts = %v, %v, %v, %v; want 5s, 10s, 15s, 60s",
			s.ReadHeaderTimeout, s.ReadTimeout, s.WriteTimeout, s.IdleTimeout)
	}
}

func TestRequestDeadlineIsTenSecondsAndKeepsStatus(t *testing.T) {
	var left time.Duration
	h := withDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, ok := r.Context().Deadline()
		if !ok {
			t.Error("request context has no deadline")
		}
		left = time.Until(d)
		w.WriteHeader(286)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if left <= 9*time.Second || left > 10*time.Second || rr.Code != 286 {
		t.Errorf("deadline in %v, status %d; want 10s and 286", left, rr.Code)
	}
}
