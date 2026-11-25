// Package writer serializes every WriteDB write through one goroutine that
// group-commits queued items.
package writer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/uptrace/bun"
)

// ErrShuttingDown is returned by Do after Stop.
var ErrShuttingDown = errors.New("writer: shutting down")

// Apply writes one item inside the batch transaction. It runs again in its own
// transaction when another item fails the batch, so its effects must stay in tx.
type Apply func(ctx context.Context, tx bun.Tx) error

// Options configures a Writer. Every field is required.
type Options struct {
	QueueCap  int           // 8192
	BatchMax  int           // 64
	BatchWait time.Duration // 15ms
	IsIOError func(error) bool
	OnFatal   func(error) // os.Exit(1) in production
	Now       func() time.Time
	After     func(time.Duration) <-chan time.Time
	// OnSyncResult is called once per sync item when its result is known;
	// wait spans enqueue to result.
	OnSyncResult func(at time.Time, wait time.Duration)
}

// Stats is the writer part of the health snapshot.
type Stats struct {
	QueueLen, QueueCap int
	DroppedTotal       int64
}

type item struct {
	apply Apply
	reply chan error // nil for async items
	enqAt time.Time
}

// Writer is the single WriteDB writer.
type Writer struct {
	db      *bun.DB
	opt     Options
	ch      chan item
	stop    chan struct{}
	done    chan struct{}
	dropped atomic.Int64

	// mu orders enqueues before Stop: senders hold RLock across the send, so
	// once Stop has closed stop no item can enter the channel after the drain.
	mu      sync.RWMutex
	stopped bool
}

func New(db *bun.DB, opt Options) *Writer {
	return &Writer{
		db:   db,
		opt:  opt,
		ch:   make(chan item, opt.QueueCap),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
}

// Run commits until Stop, then commits what is left in the channel. It also
// returns after an I/O error, once OnFatal has been called.
func (w *Writer) Run() {
	defer close(w.done)
	for {
		var first item
		select {
		case first = <-w.ch:
		case <-w.stop:
			select {
			case first = <-w.ch:
			default:
				return
			}
		}
		if !w.commit(w.collect(first)) {
			return
		}
	}
}

// collect gathers a batch: an async-only batch waits up to BatchWait for more
// items, while a sync item (or Stop) only takes what is already queued.
func (w *Writer) collect(first item) []item {
	batch := append(make([]item, 0, w.opt.BatchMax), first)
	greedy := first.reply != nil
	var timeout <-chan time.Time
	if !greedy {
		timeout = w.opt.After(w.opt.BatchWait)
	}
	for len(batch) < w.opt.BatchMax {
		if greedy {
			select {
			case it := <-w.ch:
				batch = append(batch, it)
			default:
				return batch
			}
			continue
		}
		select {
		case it := <-w.ch:
			batch = append(batch, it)
			greedy = it.reply != nil
		case <-timeout:
			return batch
		case <-w.stop:
			greedy = true
		}
	}
	return batch
}

// commit reports false after an I/O error: the commit may or may not be in
// the WAL, so no item gets a result and the process must restart.
func (w *Writer) commit(batch []item) bool {
	err := w.db.RunInTx(context.Background(), nil, func(ctx context.Context, tx bun.Tx) error {
		for _, it := range batch {
			if err := it.apply(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		w.reply(batch, nil)
		return true
	}
	if w.opt.IsIOError(err) {
		w.opt.OnFatal(err)
		return false
	}
	if len(batch) == 1 {
		w.reply(batch, err)
		return true
	}
	for _, it := range batch {
		if !w.commit([]item{it}) {
			return false
		}
	}
	return true
}

func (w *Writer) reply(batch []item, err error) {
	now := w.opt.Now()
	for _, it := range batch {
		if it.reply == nil {
			continue
		}
		w.opt.OnSyncResult(now, now.Sub(it.enqAt))
		it.reply <- err
	}
}

// Do enqueues a sync item and waits for its commit result. A full channel is
// waited on until ctx is done; once enqueued, the result is awaited regardless
// of ctx so the caller never mistakes a later commit for a failure.
func (w *Writer) Do(ctx context.Context, a Apply) error {
	it := item{apply: a, reply: make(chan error, 1), enqAt: w.opt.Now()}
	if err := w.enqueue(ctx, it); err != nil {
		return err
	}
	return <-it.reply
}

func (w *Writer) enqueue(ctx context.Context, it item) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.stopped {
		return ErrShuttingDown
	}
	select {
	case w.ch <- it:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Send enqueues an async item, dropping and counting it when the channel is
// full or the writer has stopped.
func (w *Writer) Send(a Apply) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if !w.stopped {
		select {
		case w.ch <- item{apply: a}:
			return
		default:
		}
	}
	w.dropped.Add(1)
}

// Stop rejects new items and returns after Run has committed what was queued.
// The channel is never closed so a late sender cannot panic.
func (w *Writer) Stop() {
	w.mu.Lock()
	if !w.stopped {
		w.stopped = true
		close(w.stop)
	}
	w.mu.Unlock()
	<-w.done
}

func (w *Writer) Stats() Stats {
	return Stats{QueueLen: len(w.ch), QueueCap: cap(w.ch), DroppedTotal: w.dropped.Load()}
}
