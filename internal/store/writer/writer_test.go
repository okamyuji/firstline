package writer

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	}
}

func start(t *testing.T, w *Writer) {
	t.Helper()
	go w.Run()
	t.Cleanup(w.Stop)
}

// enqueueDo starts Do calls and waits until all of them sit in the channel.
func enqueueDo(t *testing.T, w *Writer, applies []Apply) []chan error {
	t.Helper()
	res := make([]chan error, len(applies))
	for i, a := range applies {
		res[i] = make(chan error, 1)
		go func() { res[i] <- w.Do(context.Background(), a) }()
	}
	eventually(t, func() bool { return w.Stats().QueueLen == len(applies) })
	return res
}

func TestBatchesUpTo64(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := New(db, opts(c, 8192))
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
	w := New(db, opts(c, 8192))
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
	w := New(db, opts(c, 8192))
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
	w := New(db, o)
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
	w := New(db, opts(c, 8192))
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
	w := New(db, o)
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
	w := New(db, opts(c, 8192))
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
	w := New(db, opts(c, 1))
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
	w := New(db, opts(c, 1))
	w.Send(log.insert(1))
	w.Send(log.insert(2))
	w.Send(log.insert(3))
	if s := w.Stats(); s != (Stats{QueueLen: 1, QueueCap: 1, DroppedTotal: 2}) {
		t.Errorf("Stats = %+v", s)
	}
}

func TestStopDrainsAndRejects(t *testing.T) {
	db, c, log := openDB(t), newFakeClock(), &txLog{}
	w := New(db, opts(c, 8192))
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
	w := New(db, opts(c, 8192))
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
