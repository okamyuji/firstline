package app

import (
	"context"
	"fmt"
	"time"

	"github.com/okamyuji/firstline/internal/port"
)

// Reconcile feeds every payment completed since since to Completed, the same
// path a completion notice takes, so notices lost in a crash still apply.
func (s *Service) Reconcile(ctx context.Context, since time.Time) (int, error) {
	cs, err := s.Payment.Completed(ctx, since)
	if err != nil {
		return 0, fmt.Errorf("list completed payments: %w", err)
	}
	n, err := each(cs, func(c port.Completion) error { return s.Completed(ctx, c) })
	if err == nil {
		at := s.Clock()
		s.lastReconcile.Store(&at)
	}
	return n, err
}

// LastReconcileAt is the end of the last run whose every item applied; zero before one.
func (s *Service) LastReconcileAt() time.Time {
	if at := s.lastReconcile.Load(); at != nil {
		return *at
	}
	return time.Time{}
}
