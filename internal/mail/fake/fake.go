// Package fake is an in-memory port.Mailer for tests and local runs.
package fake

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/okamyuji/firstline/internal/port"
)

var errInjected = errors.New("fake mailer: injected send failure")

type Fake struct {
	mu       sync.Mutex
	sent     []port.Mail
	failNext int
}

func New() *Fake { return &Fake{} }

func (f *Fake) Send(_ context.Context, m port.Mail) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext > 0 {
		f.failNext--
		return errInjected
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *Fake) Sent() []port.Mail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// FailNext makes the next n Send calls fail without recording the mail.
func (f *Fake) FailNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
}
