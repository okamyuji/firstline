package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/auth"
	"github.com/okamyuji/firstline/internal/i18n"
	mailfake "github.com/okamyuji/firstline/internal/mail/fake"
	"github.com/okamyuji/firstline/internal/observe"
	"github.com/okamyuji/firstline/internal/observe/emf"
	"github.com/okamyuji/firstline/internal/payment/fake"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/writer"
	"github.com/okamyuji/firstline/internal/web"
)

const (
	queueCap  = 8192
	batchMax  = 64
	batchWait = 15 * time.Millisecond
)

// wiring holds the real parts of serve. The parts that need the database are
// set by open; the lifecycle's closures read them only after open succeeded.
type wiring struct {
	cfg    Config
	cs     *certStore
	rep    port.ErrorReporter
	log    *slog.Logger
	pay    *fake.Fake
	mailer port.Mailer
	rec    *observe.Recorder

	https, admin, sock *http.Server

	st  *store.Store
	w   *writer.Writer
	svc *app.Service
}

func newWiring(cfg Config, cs *certStore, rep port.ErrorReporter, log *slog.Logger) *wiring {
	wr := &wiring{
		cfg: cfg, cs: cs, rep: rep, log: log, rec: observe.NewRecorder(), mailer: mailfake.New(),
		pay:   fake.New(time.Now, time.After, cfg.FakePaymentDelay, cfg.FakePaymentOutcome),
		https: newServer(nil, log), admin: newServer(nil, log), sock: newServer(nil, log),
	}
	wr.https.TLSConfig = &tls.Config{GetCertificate: cs.getCertificate, MinVersion: tls.VersionTLS12}
	return wr
}

// open runs startup steps 2 to 4 and builds the handlers on top of them.
func (wr *wiring) open(ctx context.Context) error {
	st, err := openStore(ctx, wr.cfg)
	if err != nil {
		return err
	}
	w, err := newWriter(st, wr.rep, wr.rec.OnSyncResult, wr.log)
	if err != nil {
		return errors.Join(err, closeStore(st))
	}
	go w.Run()
	svc := app.New(app.Deps{
		Store: st, Writer: w, Inventory: inventories[wr.cfg.Inventory](), Payment: wr.pay, Errors: wr.rep, Clock: time.Now,
	})
	wr.pay.SetNotifier(svc)
	wr.st, wr.w, wr.svc = st, w, svc
	wr.https.Handler = withDeadline(web.NewHandler(web.Deps{
		App: svc, Store: st, Writer: w, I18n: i18n.New(), Tokens: auth.Tokens{Key: wr.cfg.TokenKey},
		Clock: time.Now, HoldRatePerMin: wr.cfg.HoldRatePerMin, Errors: wr.rep,
	}))
	adminMux := http.NewServeMux()
	adminMux.Handle("GET /healthz", observe.Handler(wr.log, wr.snapshot))
	wr.admin.Handler = adminMux
	wr.sock.Handler = organizerHandler(svc, reportTo(wr.rep))
	return nil
}

func (wr *wiring) snapshot() port.HealthSnapshot {
	return observe.Snapshot(wr.rec, wr.w, wr.cfg.sqliteFile(), wr.svc.LastReconcileAt, wr.cs.notAfter, time.Now())
}

func (wr *wiring) lifecycle() *lifecycle {
	l := &lifecycle{
		now: time.Now, after: time.After, check: reportTo(wr.rep),
		open:      wr.open,
		rebuild:   func(ctx context.Context) error { return wr.svc.RebuildInventory(ctx) },
		reconcile: func(ctx context.Context, since time.Time) error { return countless(wr.svc.Reconcile(ctx, since)) },
		expire:    func(ctx context.Context) error { return countless(wr.svc.ExpireDue(ctx)) },
		refunds:   func(ctx context.Context) error { return countless(wr.svc.RetryRefunds(ctx)) },
		mail: func(ctx context.Context) error {
			return countless(wr.svc.SendMail(ctx, wr.mailer, i18n.New(), auth.Tokens{Key: wr.cfg.TokenKey}, wr.cfg.BaseURL))
		},
		purge:     func(ctx context.Context) { wr.svc.PurgeSessions(ctx) },
		purgeLogs: func(ctx context.Context) { wr.svc.PurgeAccessLogs(ctx) },
		sweep:     func(context.Context) { wr.svc.Throttle.Sweep(time.Now()) },
		sample: func(context.Context) {
			s := wr.w.Stats()
			wr.rec.SampleQueue(time.Now(), s.QueueLen, s.QueueCap)
		},
		listeners: []listener{
			{"admin", startOn("tcp", wr.cfg.AdminAddr, noReady, wr.admin.Serve), wr.admin},
			{"admin socket", startUnix(wr.sock, wr.cfg.AdminSock), wr.sock},
			{"https", startOn("tcp", wr.cfg.Addr, noReady, func(ln net.Listener) error { return wr.https.ServeTLS(ln, "", "") }), wr.https},
		},
		stopPayment: wr.pay.Stop,
		stopWriter:  func() { wr.w.Stop() },
		closeStore:  func() error { return closeStore(wr.st) },
	}
	if metrics[wr.cfg.Metrics] {
		sink := emf.New(wr.cfg.EMFAddr)
		l.loops = append(l.loops, func(ctx context.Context) {
			observe.Run(ctx, l.after, emfEvery, wr.snapshot, sink, wr.log)
		})
	}
	return l
}

// countless drops the processed count of a periodic job.
func countless(_ int, err error) error { return err }

func newWriter(st *store.Store, rep port.ErrorReporter, onSync func(time.Time, time.Duration), log *slog.Logger) (*writer.Writer, error) {
	return writer.New(st.Write, writer.Options{
		QueueCap: queueCap, BatchMax: batchMax, BatchWait: batchWait,
		IsIOError: st.IsIOError,
		// After a failed fsync the commit state is unknown; exiting lets systemd
		// restart and rebuild from WriteDB, as グループコミット specifies.
		OnFatal: func(err error) {
			log.Error("write db I/O error; exiting", "error", err)
			os.Exit(1)
		},
		OnAsyncError: func(err error) { rep.Report(context.Background(), fmt.Errorf("async write: %w", err)) },
		OnSyncResult: onSync,
		Now:          time.Now,
		After:        time.After,
	})
}

// direct is the short-lived store and writer of a subcommand that writes
// without serve, under the same lock.
type direct struct {
	lock *os.File
	st   *store.Store
	w    *writer.Writer
	svc  *app.Service
}

// openDirect uses the DB inventory: the process is short-lived and serve
// rebuilds its own inventory from the DB at startup.
func openDirect(ctx context.Context, cfg Config, pay port.Payment, rep port.ErrorReporter, log *slog.Logger) (*direct, error) {
	lock, err := acquireLock(cfg.LockPath)
	if err != nil {
		return nil, err
	}
	st, err := openStore(ctx, cfg)
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	w, err := newWriter(st, rep, func(time.Time, time.Duration) {}, log)
	if err != nil {
		return nil, errors.Join(err, closeStore(st), lock.Close())
	}
	go w.Run()
	svc := app.New(app.Deps{Store: st, Writer: w, Inventory: inventories["db"](), Payment: pay, Errors: rep, Clock: time.Now})
	return &direct{lock: lock, st: st, w: w, svc: svc}, nil
}

func (d *direct) close() error {
	d.w.Stop()
	return errors.Join(closeStore(d.st), d.lock.Close())
}
