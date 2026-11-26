package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/port"
)

const (
	// Three Shutdowns share 20s of systemd's 30s TimeoutStopSec; steps 2 to 4 get the rest.
	shutdownGrace   = 20 * time.Second
	requestDeadline = 10 * time.Second

	startupReconcileWindow = 24 * time.Hour
	reconcileWindow        = 15 * time.Minute

	expireEvery    = time.Second
	refundEvery    = 30 * time.Second
	reconcileEvery = 30 * time.Second
	mailEvery      = 5 * time.Second
	purgeEvery     = time.Hour
	purgeLogsEvery = 24 * time.Hour
	sweepEvery     = time.Minute
	sampleEvery    = time.Second
	emfEvery       = 60 * time.Second
)

// listener is one of the three http.Servers. start listens before it returns
// and serves in the background, sending Serve's result to errc.
type listener struct {
	name  string
	start func(errc chan<- error) error
	srv   interface{ Shutdown(context.Context) error }
}

type job struct {
	name  string
	every time.Duration
	run   func(context.Context)
}

// lifecycle runs startup steps 2 to 8 and shutdown steps 1 to 4 in the order
// the spec gives. wiring fills it with the real parts; tests fill it with
// parts that record each call.
type lifecycle struct {
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
	check checkFunc

	open                  func(context.Context) error // steps 2 to 4
	rebuild               func(context.Context) error
	reconcile             func(context.Context, time.Time) error
	expire, refunds, mail func(context.Context) error
	purge, sweep, sample  func(context.Context)
	purgeLogs             func(context.Context)
	loops                 []func(context.Context) // background loops besides the jobs
	listeners             []listener              // in step 8's order

	stopPayment func()
	stopWriter  func()
	closeStore  func() error
}

func (l *lifecycle) jobs() []job {
	return []job{
		{"expire holds", expireEvery, func(ctx context.Context) { l.check(ctx, "expire holds", l.expire(ctx)) }},
		{"retry refunds", refundEvery, func(ctx context.Context) { l.check(ctx, "retry refunds", l.refunds(ctx)) }},
		{"reconcile payments", reconcileEvery, func(ctx context.Context) {
			l.check(ctx, "reconcile payments", l.reconcile(ctx, l.now().Add(-reconcileWindow)))
		}},
		{"send mail", mailEvery, func(ctx context.Context) { l.check(ctx, "send mail", l.mail(ctx)) }},
		{"purge sessions", purgeEvery, l.purge},
		{"purge access logs", purgeLogsEvery, l.purgeLogs},
		{"sweep throttle", sweepEvery, l.sweep},
		{"sample queue", sampleEvery, l.sample},
	}
}

// run returns after SIGTERM on term or a listener failure, once shutdown is done.
func (l *lifecycle) run(term <-chan os.Signal) error {
	ctx := context.Background()
	if err := l.open(ctx); err != nil {
		return err
	}
	bg, stopBG := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		l.stopPayment()
		stopBG()
		wg.Wait()
		l.stopWriter()
		l.check(ctx, "close store", l.closeStore())
	}()
	if err := l.rebuild(ctx); err != nil {
		return fmt.Errorf("rebuild inventory: %w", err)
	}
	// Reconcile first: a hold whose payment completed in time while serve was
	// down must become purchased before the expiry pass sees it.
	l.check(ctx, "startup reconcile", l.reconcile(ctx, l.now().Add(-startupReconcileWindow)))
	l.check(ctx, "startup expire", l.expire(ctx))
	l.check(ctx, "startup refunds", l.refunds(ctx))
	for _, j := range l.jobs() {
		wg.Go(func() { app.Every(bg, l.after, j.every, j.run) })
	}
	for _, f := range l.loops {
		wg.Go(func() { f(bg) })
	}
	return l.serve(term)
}

func (l *lifecycle) serve(term <-chan os.Signal) error {
	errc := make(chan error, len(l.listeners))
	var err error
	for _, s := range l.listeners {
		if err = s.start(errc); err != nil {
			err = fmt.Errorf("start %s: %w", s.name, err)
			break
		}
	}
	if err == nil {
		select {
		case <-term:
		case err = <-errc:
		}
	}
	l.shutdown()
	return err
}

// shutdown stops the three servers in parallel under one deadline.
func (l *lifecycle) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	var wg sync.WaitGroup
	for _, s := range l.listeners {
		wg.Go(func() { l.check(ctx, "shut down "+s.name, s.srv.Shutdown(ctx)) })
	}
	wg.Wait()
}

func reportTo(r port.ErrorReporter) checkFunc {
	return func(ctx context.Context, what string, err error) {
		if err != nil {
			r.Report(ctx, fmt.Errorf("%s: %w", what, err))
		}
	}
}

func newServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// withDeadline gives each request a 10s context. Unlike http.TimeoutHandler it
// neither buffers the response nor answers 503 while a handler still waits for
// a commit it already sent, as 期限と待ち方 requires.
func withDeadline(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestDeadline)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// startOn listens, runs ready, and then serves in the background. A failed
// ready closes the listener before anything is served.
func startOn(network, addr string, ready func() error, serve func(net.Listener) error) func(chan<- error) error {
	return func(errc chan<- error) error {
		ln, err := net.Listen(network, addr)
		if err != nil {
			return err
		}
		if err := ready(); err != nil {
			_ = ln.Close()
			return err
		}
		go func() { errc <- serve(ln) }()
		return nil
	}
}

func noReady() error { return nil }

// startUnix restricts the socket to its owner before serving it. Step 1 has
// already removed a stale socket with removeStaleSocket.
func startUnix(s *http.Server, path string) func(chan<- error) error {
	return func(errc chan<- error) error {
		return startOn("unix", path, func() error { return os.Chmod(path, 0o600) }, s.Serve)(errc)
	}
}

// removeStaleSocket removes a socket file left by a crash, on which Listen
// would fail. The flock this process holds means no live serve owns it.
// Anything else at the path is the operator's file and stops the start.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("FIRSTLINE_ADMIN_SOCK: %w", err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("FIRSTLINE_ADMIN_SOCK: %s exists and is not a socket", path)
	}
	return os.Remove(path)
}

func serveCmd(_ context.Context, e env, _ []string) int {
	// Created before anything else: a SIGHUP with no handler kills the process,
	// and Restart=on-failure does not restart that exit.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	return exitCode(e.stderr, "serve", serveWith(e, term, hup))
}

// serveWith runs startup step 1 and hands the rest to the lifecycle.
func serveWith(e env, term, hup <-chan os.Signal) (err error) {
	cfg, err := LoadConfig(e.getenv)
	if err != nil {
		return err
	}
	lock, err := acquireLock(cfg.LockPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err := removeStaleSocket(cfg.AdminSock); err != nil {
		return err
	}
	cs, err := loadCert(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return err
	}
	rep := newReporter(cfg.ErrorReporters, e.log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchHUP(ctx, hup, cs, reportTo(rep))
	return newWiring(cfg, cs, rep, e.log).lifecycle().run(term)
}
