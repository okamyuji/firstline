package fake

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/port"
)

var _ port.Payment = (*Fake)(nil)

type timerReq struct {
	d  time.Duration
	ch chan time.Time
}

// manual hands every After request to the test, which fires it explicitly.
type manual struct{ reqs chan timerReq }

func newManual() *manual { return &manual{reqs: make(chan timerReq, 16)} }

func (m *manual) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	m.reqs <- timerReq{d, ch}
	return ch
}

func (m *manual) next(t *testing.T) timerReq {
	t.Helper()
	select {
	case r := <-m.reqs:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no timer requested")
		return timerReq{}
	}
}

type notifier struct {
	mu        sync.Mutex
	completed []port.Completion
	failed    [][2]string
	errs      int // number of calls to fail before succeeding
	calls     chan struct{}
}

func newNotifier() *notifier { return &notifier{calls: make(chan struct{}, 64)} }

func (n *notifier) result() error {
	if n.errs > 0 {
		n.errs--
		return errors.New("busy")
	}
	return nil
}

func (n *notifier) Completed(_ context.Context, c port.Completion) error {
	n.mu.Lock()
	err := n.result()
	if err == nil {
		n.completed = append(n.completed, c)
	}
	n.mu.Unlock()
	n.calls <- struct{}{}
	return err
}

func (n *notifier) Failed(_ context.Context, purchaseID, paymentID string) error {
	n.mu.Lock()
	err := n.result()
	if err == nil {
		n.failed = append(n.failed, [2]string{purchaseID, paymentID})
	}
	n.mu.Unlock()
	n.calls <- struct{}{}
	return err
}

func (n *notifier) wait(t *testing.T) {
	t.Helper()
	select {
	case <-n.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("notifier not called")
	}
}

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newFake(t *testing.T, outcome Outcome) (*Fake, *manual, *time.Time) {
	t.Helper()
	m := newManual()
	now := t0
	f := New(func() time.Time { return now }, m.after, 3*time.Second, outcome)
	t.Cleanup(f.Stop)
	return f, m, &now
}

func TestStartReturnsPaymentIDAndEmptyURL(t *testing.T) {
	f, _, _ := newFake(t, Complete)
	id1, url, err := f.Start(context.Background(), "p1", 100)
	if err != nil || url != "" || !strings.HasPrefix(id1, "pay_") || len(id1) <= len("pay_") {
		t.Fatalf("Start = %q %q %v", id1, url, err)
	}
	id2, _, _ := f.Start(context.Background(), "p2", 100)
	if id1 == id2 {
		t.Fatal("payment IDs must differ")
	}
}

func TestFailStartFailsOnlyNextStart(t *testing.T) {
	f, _, _ := newFake(t, Complete)
	want := errors.New("boom")
	f.FailStart(want)
	if _, _, err := f.Start(context.Background(), "p1", 1); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := f.Start(context.Background(), "p1", 1); err != nil {
		t.Fatal(err)
	}
}

func TestCompletionNotifiedAfterDelayWithCompletionTime(t *testing.T) {
	f, m, now := newFake(t, Complete)
	n := newNotifier()
	f.SetNotifier(n)
	id, _, _ := f.Start(context.Background(), "p1", 250)
	r := m.next(t)
	if r.d != 3*time.Second {
		t.Fatalf("delay = %v", r.d)
	}
	*now = t0.Add(3 * time.Second)
	r.ch <- *now
	n.wait(t)
	want := port.Completion{PurchaseID: "p1", PaymentID: id, Amount: 250, At: t0.Add(3 * time.Second)}
	if len(n.completed) != 1 || n.completed[0] != want {
		t.Fatalf("completed = %v want %v", n.completed, want)
	}
}

func TestFailedOutcomeNotifiesFailedAndIsNotListed(t *testing.T) {
	f, m, _ := newFake(t, Fail)
	n := newNotifier()
	f.SetNotifier(n)
	id, _, _ := f.Start(context.Background(), "p1", 1)
	m.next(t).ch <- t0
	n.wait(t)
	if len(n.failed) != 1 || n.failed[0] != [2]string{"p1", id} {
		t.Fatalf("failed = %v", n.failed)
	}
	got, _ := f.Completed(context.Background(), time.Time{})
	if len(got) != 0 {
		t.Fatalf("Completed = %v", got)
	}
}

func TestNotifierErrorRetriesEvery5SecondsUntilSuccess(t *testing.T) {
	f, m, _ := newFake(t, Complete)
	n := newNotifier()
	n.errs = 2
	f.SetNotifier(n)
	_, _, _ = f.Start(context.Background(), "p1", 1)
	m.next(t).ch <- t0
	for range 2 {
		n.wait(t)
		r := m.next(t)
		if r.d != 5*time.Second {
			t.Fatalf("retry delay = %v", r.d)
		}
		r.ch <- t0
	}
	n.wait(t)
	// A further timer request would mean a retry after success.
	select {
	case r := <-m.reqs:
		t.Fatalf("unexpected timer %v after success", r.d)
	case <-time.After(50 * time.Millisecond):
	}
	if len(n.completed) != 1 {
		t.Fatalf("completed = %v", n.completed)
	}
}

func TestCompletedFiltersBySinceAndSortsByAt(t *testing.T) {
	f, m, now := newFake(t, Complete)
	f.SetNotifier(newNotifier())
	ctx := context.Background()
	for i, pid := range []string{"p1", "p2", "p3"} {
		_, _, _ = f.Start(ctx, pid, i+1)
		*now = t0.Add(time.Duration(i) * time.Minute)
		r := m.next(t)
		r.ch <- *now
		// wait until recorded
		for {
			if got, _ := f.Completed(ctx, time.Time{}); len(got) == i+1 {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	got, _ := f.Completed(ctx, t0.Add(time.Minute))
	if len(got) != 2 || got[0].PurchaseID != "p2" || got[1].PurchaseID != "p3" {
		t.Fatalf("Completed = %v", got)
	}
}

func TestRefundIsIdempotentAndRejectsUnknownID(t *testing.T) {
	f, _, _ := newFake(t, Complete)
	ctx := context.Background()
	id, _, _ := f.Start(ctx, "p1", 1)
	for range 2 {
		if err := f.Refund(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.Refunds(id); got != 1 {
		t.Fatalf("Refunds = %d", got)
	}
	if err := f.Refund(ctx, "pay_unknown"); err == nil {
		t.Fatal("want error for unknown id")
	}
	if f.Refunds("pay_unknown") != 0 {
		t.Fatal("unknown id recorded")
	}
}

func TestPendingNotificationSentAfterSetNotifier(t *testing.T) {
	f, m, _ := newFake(t, Complete)
	_, _, _ = f.Start(context.Background(), "p1", 1)
	m.next(t).ch <- t0
	n := newNotifier()
	select {
	case <-n.calls:
		t.Fatal("notified before SetNotifier")
	case <-time.After(50 * time.Millisecond):
	}
	f.SetNotifier(n)
	n.wait(t)
	if len(n.completed) != 1 {
		t.Fatalf("completed = %v", n.completed)
	}
}

func TestStopPreventsNotificationsAndWaitsForGoroutines(t *testing.T) {
	f, m, _ := newFake(t, Complete)
	n := newNotifier()
	f.SetNotifier(n)
	_, _, _ = f.Start(context.Background(), "p1", 1)
	r := m.next(t)
	f.Stop()
	r.ch <- t0
	select {
	case <-n.calls:
		t.Fatal("notified after Stop")
	case <-time.After(50 * time.Millisecond):
	}
	f.Stop() // second Stop is safe
}

func TestStopInterruptsRetryWait(t *testing.T) {
	f, m, _ := newFake(t, Complete)
	n := newNotifier()
	n.errs = 100
	f.SetNotifier(n)
	_, _, _ = f.Start(context.Background(), "p1", 1)
	m.next(t).ch <- t0
	n.wait(t)
	m.next(t) // retry timer pending
	done := make(chan struct{})
	go func() { f.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
}

func TestStartAfterStopSpawnsNothing(t *testing.T) {
	f, m, _ := newFake(t, Complete)
	f.Stop()
	if _, _, err := f.Start(context.Background(), "p1", 1); err == nil {
		t.Fatal("want error after Stop")
	}
	select {
	case <-m.reqs:
		t.Fatal("timer requested after Stop")
	default:
	}
}

// The clock moves before every retry; each notification must still carry the
// completion time. An atomic clock keeps the check independent of -race.
func TestCompletionTimeStaysAcrossRetries(t *testing.T) {
	m := newManual()
	var now atomic.Int64
	now.Store(t0.UnixMilli())
	f := New(func() time.Time { return time.UnixMilli(now.Load()).UTC() }, m.after, 3*time.Second, Complete)
	t.Cleanup(f.Stop)
	n := newNotifier()
	n.errs = 2
	f.SetNotifier(n)
	_, _, _ = f.Start(context.Background(), "p1", 1)
	now.Store(t0.Add(3 * time.Second).UnixMilli())
	m.next(t).ch <- t0
	for i := range 2 {
		n.wait(t)
		now.Store(t0.Add(time.Duration(i+1) * time.Minute).UnixMilli())
		m.next(t).ch <- t0
	}
	n.wait(t)
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.completed) != 1 || !n.completed[0].At.Equal(t0.Add(3*time.Second)) {
		t.Fatalf("completed = %v, want one completion at %v", n.completed, t0.Add(3*time.Second))
	}
}

// When Stop and the timer race, sleep must report the stop even if the timer
// case wins the select, or a notification goes out after Stop.
func TestTimerFiringDuringStopSendsNothing(t *testing.T) {
	for range 64 {
		n := newNotifier()
		f := New(func() time.Time { return t0 }, nil, 3*time.Second, Complete)
		f.after = func(time.Duration) <-chan time.Time {
			f.cancel()
			ch := make(chan time.Time, 1)
			ch <- t0
			return ch
		}
		f.SetNotifier(n)
		_, _, _ = f.Start(context.Background(), "p1", 1)
		f.Stop()
		select {
		case <-n.calls:
			t.Fatal("notified after Stop")
		default:
		}
	}
}
