package writer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/mysql"
	"github.com/okamyuji/firstline/internal/store/postgres"
	"github.com/okamyuji/firstline/internal/store/servertest"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite"
)

type row struct {
	bun.BaseModel `bun:"table:items"`
	ID            int64 `bun:",pk"`
}

func openDB(t *testing.T) *bun.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "w.db") + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := bun.NewDB(sqldb, sqlitedialect.New())
	t.Cleanup(func() { _ = db.Close() })
	return createItems(t, db)
}

func createItems(t *testing.T, db *bun.DB) *bun.DB {
	t.Helper()
	if _, err := db.NewCreateTable().Model((*row)(nil)).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	return db
}

func countRows(t *testing.T, db *bun.DB) int {
	t.Helper()
	n, err := db.NewSelect().Model((*row)(nil)).Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

// eventually polls cond because commits happen on the writer goroutine.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}

type fakeClock struct {
	mu         sync.Mutex
	now        time.Time
	waiters    []waiter
	registered chan time.Duration
}

type waiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC), registered: make(chan time.Duration, 1024)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.waiters = append(c.waiters, waiter{at: c.now.Add(d), ch: ch})
	c.registered <- d
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(c.now) {
			kept = append(kept, w)
			continue
		}
		w.ch <- c.now
	}
	c.waiters = kept
}

// txLog records which transaction each successful apply ran in.
type txLog struct {
	mu   sync.Mutex
	byTx map[*sql.Tx]int
}

func (l *txLog) insert(id int64) Apply {
	return func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(&row{ID: id}).Exec(ctx); err != nil {
			return err
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.byTx == nil {
			l.byTx = map[*sql.Tx]int{}
		}
		l.byTx[tx.Tx]++
		return nil
	}
}

func (l *txLog) sizes() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var s []int
	for _, n := range l.byTx {
		s = append(s, n)
	}
	return s
}

var errIO = errors.New("disk I/O error")

func opts(c *fakeClock, queueCap int) Options {
	return Options{
		QueueCap:     queueCap,
		BatchMax:     64,
		BatchWait:    15 * time.Millisecond,
		IsIOError:    func(err error) bool { return errors.Is(err, errIO) },
		OnFatal:      func(error) {},
		Now:          c.Now,
		After:        c.After,
		OnSyncResult: func(time.Time, time.Duration) {},
		OnAsyncError: func(error) {},
	}
}

func newWriter(t *testing.T, db *bun.DB, o Options) *Writer {
	t.Helper()
	w, err := New(db, o)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func start(t *testing.T, w *Writer) {
	t.Helper()
	go w.Run()
	t.Cleanup(w.Stop)
}

// enqueueDo starts Do calls one by one so the channel holds them in slice order.
func enqueueDo(t *testing.T, w *Writer, applies []Apply) []chan error {
	t.Helper()
	res := make([]chan error, len(applies))
	queued := w.Stats().QueueLen
	for i, a := range applies {
		res[i] = make(chan error, 1)
		go func() { res[i] <- w.Do(context.Background(), a) }()
		eventually(t, func() bool { return w.Stats().QueueLen == queued+i+1 })
	}
	return res
}

func TestBatchesUpTo64(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	for i := range 200 {
		w.Send(log.insert(int64(i)))
	}
	start(t, w)
	eventually(t, func() bool {
		c.Advance(15 * time.Millisecond)
		return countRows(t, db) == 200
	})
	sizes := log.sizes()
	if len(sizes) < 4 {
		t.Errorf("commits = %d, want >= 4", len(sizes))
	}
	for _, n := range sizes {
		if n > 64 {
			t.Errorf("batch of %d items, want <= 64", n)
		}
	}
}

func TestFlushesAfter15ms(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	start(t, w)
	w.Send(log.insert(1))
	select {
	case d := <-c.registered:
		if d != 15*time.Millisecond {
			t.Fatalf("timer = %v, want 15ms", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start the batch timer")
	}
	c.Advance(14 * time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if n := countRows(t, db); n != 0 {
		t.Fatalf("rows before 15ms = %d, want 0", n)
	}
	c.Advance(time.Millisecond)
	eventually(t, func() bool { return countRows(t, db) == 1 })
}

func TestSyncItemFlushesWithoutWaiting(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	start(t, w)
	if err := w.Do(t.Context(), log.insert(1)); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
	if len(c.registered) != 0 {
		t.Error("sync item started the batch timer")
	}
}

func TestSyncItemsShareOneCommit(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	o := opts(c, 8192)
	var mu sync.Mutex
	var waits []time.Duration
	var ats []time.Time
	o.OnSyncResult = func(at time.Time, wait time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		ats, waits = append(ats, at), append(waits, wait)
	}
	w := newWriter(t, db, o)
	applies := make([]Apply, 50)
	for i := range applies {
		applies[i] = log.insert(int64(i))
	}
	res := enqueueDo(t, w, applies)
	c.Advance(7 * time.Millisecond)
	start(t, w)
	for _, r := range res {
		if err := <-r; err != nil {
			t.Fatal(err)
		}
	}
	if sizes := log.sizes(); len(sizes) != 1 || sizes[0] != 50 {
		t.Errorf("tx sizes = %v, want [50]", sizes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 50 {
		t.Fatalf("OnSyncResult calls = %d, want 50", len(waits))
	}
	for i := range waits {
		if waits[i] != 7*time.Millisecond || !ats[i].Equal(c.Now()) {
			t.Fatalf("OnSyncResult(%v, %v), want (%v, 7ms)", ats[i], waits[i], c.Now())
		}
	}
}

func TestFailedItemGetsItsOwnError(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	if _, err := db.NewInsert().Model(&row{ID: 30}).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	w := newWriter(t, db, opts(c, 8192))
	applies := make([]Apply, 64)
	for i := range applies {
		applies[i] = log.insert(int64(i + 1))
	}
	res := enqueueDo(t, w, applies)
	start(t, w)
	for i, r := range res {
		err := <-r
		if (i+1 == 30) != (err != nil) {
			t.Errorf("item %d: err = %v", i+1, err)
		}
	}
	if n := countRows(t, db); n != 64 {
		t.Errorf("rows = %d, want 64", n)
	}
}

func TestIOErrorCallsOnFatalAndRepliesNothing(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	o := opts(c, 8192)
	var fatal atomic.Int32
	o.OnFatal = func(err error) {
		if !errors.Is(err, errIO) {
			t.Errorf("OnFatal(%v), want errIO", err)
		}
		fatal.Add(1)
	}
	w := newWriter(t, db, o)
	res := enqueueDo(t, w, []Apply{
		log.insert(1),
		func(context.Context, bun.Tx) error { return errIO },
		log.insert(2),
	})
	done := make(chan struct{})
	go func() { w.Run(); close(done) }()
	<-done
	if n := fatal.Load(); n != 1 {
		t.Errorf("OnFatal calls = %d, want 1", n)
	}
	time.Sleep(5 * time.Millisecond)
	for i, r := range res {
		select {
		case err := <-r:
			t.Errorf("item %d got reply %v, want none", i, err)
		default:
		}
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

func TestDoWaitsAfterEnqueueEvenIfCtxDone(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	ctx, cancel := context.WithCancel(t.Context())
	res := make(chan error, 1)
	go func() { res <- w.Do(ctx, log.insert(1)) }()
	eventually(t, func() bool { return w.Stats().QueueLen == 1 })
	cancel()
	start(t, w)
	if err := <-res; err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	if n := countRows(t, db); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestDoReturnsCtxErrWhenQueueFull(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 1))
	w.Send(log.insert(1))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	if err := w.Do(ctx, log.insert(2)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do = %v, want DeadlineExceeded", err)
	}
	if s := w.Stats(); s.QueueLen != 1 || s.DroppedTotal != 0 {
		t.Errorf("Stats = %+v, want QueueLen 1, DroppedTotal 0", s)
	}
	start(t, w)
	w.Stop()
	if n := countRows(t, db); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestSendDropsWhenFull(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 1))
	w.Send(log.insert(1))
	w.Send(log.insert(2))
	w.Send(log.insert(3))
	if s := w.Stats(); s != (Stats{QueueLen: 1, QueueCap: 1, DroppedTotal: 2}) {
		t.Errorf("Stats = %+v", s)
	}
}

func TestStopDrainsAndRejects(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	res := enqueueDo(t, w, []Apply{log.insert(100)})
	for i := range 100 {
		w.Send(log.insert(int64(i)))
	}
	go w.Run()
	w.Stop()
	if err := <-res[0]; err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db); n != 101 {
		t.Errorf("rows = %d, want 101", n)
	}
	if err := w.Do(t.Context(), log.insert(200)); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Do after Stop = %v, want ErrShuttingDown", err)
	}
	w.Send(log.insert(201))
	if s := w.Stats(); s.DroppedTotal != 1 || s.QueueLen != 0 {
		t.Errorf("Stats after Stop = %+v, want DroppedTotal 1, QueueLen 0", s)
	}
	w.Stop()
}

func TestSyncItemEndsTimerWait(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	start(t, w)
	w.Send(log.insert(1))
	<-c.registered
	if err := w.Do(t.Context(), log.insert(2)); err != nil {
		t.Fatal(err)
	}
	if sizes := log.sizes(); len(sizes) != 1 || sizes[0] != 2 {
		t.Errorf("tx sizes = %v, want [2]", sizes)
	}
}

// commitCounter counts real transaction commits; savepoint releases run as RELEASE SAVEPOINT.
type commitCounter struct{ n atomic.Int32 }

func (c *commitCounter) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (c *commitCounter) AfterQuery(_ context.Context, e *bun.QueryEvent) {
	if e.Query == "COMMIT" {
		c.n.Add(1)
	}
}

func TestFailedItemsRollBackToTheirSavepoints(t *testing.T) {
	testFailedItemsRollBack(t, openDB(t))
}

// PostgreSQL aborts the whole transaction on a failed statement, so the items after a
// database error commit only if the writer rolls back to the item's savepoint.
func TestFailedItemsRollBackOnServers(t *testing.T) {
	for _, d := range []struct {
		name   string
		server func(*testing.T) *servertest.Server
		open   func(context.Context, string) (*store.Store, error)
	}{{"postgres", servertest.Postgres, postgres.Open}, {"mysql", servertest.MySQL, mysql.Open}} {
		t.Run(d.name, func(t *testing.T) {
			s, err := d.open(t.Context(), d.server(t).DB(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
			testFailedItemsRollBack(t, createItems(t, s.Write))
		})
	}
}

// testFailedItemsRollBack fails odd items after their insert: items 1, 5, 9... with a Go
// error, items 3, 7, 11... with the database's duplicate key error.
func testFailedItemsRollBack(t *testing.T, db *bun.DB) {
	c, log := newFakeClock(), &txLog{}
	commits := &commitCounter{}
	db = db.WithQueryHook(commits)
	w := newWriter(t, db, opts(c, 8192))
	applies := make([]Apply, 64)
	for i := range applies {
		ins := log.insert(int64(i))
		applies[i] = ins
		if i%2 == 1 {
			applies[i] = func(ctx context.Context, tx bun.Tx) error {
				if err := ins(ctx, tx); err != nil {
					return err
				}
				if i%4 == 3 {
					if err := ins(ctx, tx); err == nil {
						return errors.New("duplicate insert succeeded")
					}
				}
				return fmt.Errorf("item %d", i)
			}
		}
	}
	res := enqueueDo(t, w, applies)
	start(t, w)
	for i, r := range res {
		err := <-r
		want := ""
		if i%2 == 1 {
			want = fmt.Sprintf("item %d", i)
		}
		if got := fmt.Sprint(err); err == nil && want != "" || err != nil && got != want {
			t.Errorf("item %d: err = %v, want %q", i, err, want)
		}
	}
	if n := countRows(t, db); n != 32 {
		t.Errorf("rows = %d, want 32", n)
	}
	if n := commits.n.Load(); n != 1 {
		t.Errorf("commits = %d, want 1", n)
	}
}

func TestIOErrorAfterFailedItemStopsTheBatch(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	o := opts(c, 8192)
	var fatal atomic.Int32
	o.OnFatal = func(error) { fatal.Add(1) }
	w := newWriter(t, db, o)
	res := enqueueDo(t, w, []Apply{
		log.insert(1),
		func(context.Context, bun.Tx) error { return errors.New("sold out") },
		func(context.Context, bun.Tx) error { return errIO },
		log.insert(2),
	})
	done := make(chan struct{})
	go func() { w.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going after the I/O error")
	}
	if fatal.Load() != 1 || countRows(t, db) != 0 {
		t.Errorf("fatal = %d, rows = %d, want 1 and 0", fatal.Load(), countRows(t, db))
	}
	for i, r := range res {
		select {
		case err := <-r:
			t.Errorf("item %d got reply %v, want none", i, err)
		default:
		}
	}
}

// Run with -race: without the RLock in Send or enqueue, a send races Stop's drain.
func TestStopConcurrentWithSendAndDo(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	go w.Run()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Go(func() {
			for i := range 20 {
				id := int64(g*1000 + i)
				w.Send(log.insert(-id - 1))
				if err := w.Do(context.Background(), log.insert(id)); err != nil && !errors.Is(err, ErrShuttingDown) {
					t.Error(err)
				}
			}
		})
	}
	time.Sleep(time.Millisecond)
	w.Stop()
	wg.Wait()
}

func TestAsyncItemErrorGoesToOnAsyncError(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	o := opts(c, 8192)
	got := make(chan error, 3)
	o.OnAsyncError = func(err error) { got <- err }
	w := newWriter(t, db, o)
	failed := errors.New("bad sql")
	w.Send(func(context.Context, bun.Tx) error { return failed })
	w.Send(log.insert(1))
	res := enqueueDo(t, w, []Apply{func(context.Context, bun.Tx) error { return errors.New("sold out") }})
	go w.Run()
	w.Stop()
	if err := <-res[0]; err == nil {
		t.Error("sync item: err = nil, want its error")
	}
	close(got)
	var errs []error
	for err := range got {
		errs = append(errs, err)
	}
	if len(errs) != 1 || !errors.Is(errs[0], failed) {
		t.Errorf("OnAsyncError calls = %v, want [%v]", errs, failed)
	}
	if n := countRows(t, db); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestNewRejectsInvalidOptions(t *testing.T) {
	c := newFakeClock()
	cases := map[string]func(*Options){
		"nil IsIOError":     func(o *Options) { o.IsIOError = nil },
		"nil OnFatal":       func(o *Options) { o.OnFatal = nil },
		"nil Now":           func(o *Options) { o.Now = nil },
		"nil After":         func(o *Options) { o.After = nil },
		"nil OnSyncResult":  func(o *Options) { o.OnSyncResult = nil },
		"nil OnAsyncError":  func(o *Options) { o.OnAsyncError = nil },
		"zero QueueCap":     func(o *Options) { o.QueueCap = 0 },
		"zero BatchMax":     func(o *Options) { o.BatchMax = 0 },
		"zero BatchWait":    func(o *Options) { o.BatchWait = 0 },
		"negative QueueCap": func(o *Options) { o.QueueCap = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			o := opts(c, 8192)
			mutate(&o)
			if w, err := New(nil, o); err == nil || w != nil {
				t.Errorf("New = %v, %v; want nil and an error", w, err)
			}
		})
	}
	if _, err := New(nil, opts(c, 1)); err != nil {
		t.Errorf("New with minimal valid options = %v", err)
	}
}

func TestBatchErrorOutsideItemsFailsEveryItem(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	o := opts(c, 8192)
	o.OnFatal = func(err error) { t.Errorf("OnFatal(%v) for a non-I/O error", err) }
	w := newWriter(t, db, o)
	res := enqueueDo(t, w, []Apply{
		log.insert(1),
		// Ending the transaction from inside an item removes its savepoint, so the release fails.
		func(ctx context.Context, tx bun.Tx) error {
			_, err := tx.ExecContext(ctx, "ROLLBACK")
			return err
		},
		log.insert(2),
	})
	start(t, w)
	for i, r := range res {
		if err := <-r; err == nil {
			t.Errorf("item %d: err = nil, want the batch error", i)
		}
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}

// A failed ROLLBACK TO must fail the batch; bun's Tx.RunInTx would drop that error.
// The item's own error stays in the batch error: SQLite ends the transaction by
// itself on errors such as SQLITE_FULL, and the cause must not be lost.
func TestFailedSavepointRollbackFailsEveryItem(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := newWriter(t, db, opts(c, 8192))
	errSoldOut := errors.New("sold out")
	res := enqueueDo(t, w, []Apply{
		log.insert(1),
		func(ctx context.Context, tx bun.Tx) error {
			if _, err := tx.ExecContext(ctx, "ROLLBACK"); err != nil {
				return err
			}
			return errSoldOut
		},
		log.insert(2),
	})
	start(t, w)
	for i, r := range res {
		if err := <-r; !errors.Is(err, errSoldOut) {
			t.Errorf("item %d: err = %v, want the batch error holding the item error", i, err)
		}
	}
	if n := countRows(t, db); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}
