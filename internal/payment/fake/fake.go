// Package fake is an in-memory port.Payment that completes or fails each
// payment after a delay and pushes the result to a PaymentNotifier.
package fake

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/okamyuji/firstline/internal/port"
)

type Outcome int

const (
	Complete Outcome = iota
	Fail
)

// retryInterval is the notifier retry period fixed by the payment contract.
const retryInterval = 5 * time.Second

var (
	errStopped        = errors.New("fake payment: stopped")
	errUnknownPayment = errors.New("fake payment: unknown payment id")
)

type Fake struct {
	clock   func() time.Time
	after   func(time.Duration) <-chan time.Time
	delay   time.Duration
	outcome Outcome

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	ready  chan struct{} // closed by SetNotifier

	mu        sync.Mutex
	notifier  port.PaymentNotifier
	stopped   bool
	failStart error
	refunds   map[string]int
	done      []port.Completion
}

// New builds a Fake. production passes time.Now and time.After; tests pass
// controllable replacements so neither the delay nor the retry sleeps.
func New(clock func() time.Time, after func(time.Duration) <-chan time.Time, delay time.Duration, outcome Outcome) *Fake {
	ctx, cancel := context.WithCancel(context.Background())
	return &Fake{
		clock: clock, after: after, delay: delay, outcome: outcome,
		ctx: ctx, cancel: cancel,
		ready:   make(chan struct{}),
		refunds: map[string]int{},
	}
}

// SetNotifier must be called once. Notifications created before it are held
// until it runs.
func (f *Fake) SetNotifier(n port.PaymentNotifier) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notifier != nil {
		return
	}
	f.notifier = n
	close(f.ready)
}

// Stop ends all notification goroutines and waits for them to exit.
func (f *Fake) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	f.cancel()
	f.wg.Wait()
}

func (f *Fake) FailStart(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStart = err
}

// Refunds reports how many refunds were made for paymentID.
func (f *Fake) Refunds(paymentID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refunds[paymentID]
}

func (f *Fake) Start(_ context.Context, purchaseID string, amount int) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return "", "", errStopped
	}
	if err := f.failStart; err != nil {
		f.failStart = nil
		return "", "", err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("fake payment: generate id: %w", err)
	}
	id := "pay_" + base64.RawURLEncoding.EncodeToString(b)
	f.refunds[id] = 0
	f.wg.Add(1)
	go f.run(purchaseID, id, amount)
	return id, "", nil
}

func (f *Fake) run(purchaseID, paymentID string, amount int) {
	defer f.wg.Done()
	if !f.sleep(f.delay) {
		return
	}
	c := port.Completion{PurchaseID: purchaseID, PaymentID: paymentID, Amount: amount, At: f.clock()}
	if f.outcome == Complete {
		f.mu.Lock()
		f.done = append(f.done, c)
		f.mu.Unlock()
	}
	select {
	case <-f.ready:
	case <-f.ctx.Done():
		return
	}
	for {
		var err error
		if f.outcome == Complete {
			err = f.notifier.Completed(f.ctx, c)
		} else {
			err = f.notifier.Failed(f.ctx, purchaseID, paymentID)
		}
		if err == nil || !f.sleep(retryInterval) {
			return
		}
	}
}

// sleep reports false when Stop interrupted the wait.
func (f *Fake) sleep(d time.Duration) bool {
	select {
	case <-f.after(d):
		return f.ctx.Err() == nil
	case <-f.ctx.Done():
		return false
	}
}

func (f *Fake) Refund(_ context.Context, paymentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.refunds[paymentID]
	if !ok {
		return errUnknownPayment
	}
	if n == 0 {
		f.refunds[paymentID] = 1
	}
	return nil
}

func (f *Fake) Completed(_ context.Context, since time.Time) ([]port.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []port.Completion
	for _, c := range f.done {
		if !c.At.Before(since) {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b port.Completion) int { return a.At.Compare(b.At) })
	return out, nil
}
