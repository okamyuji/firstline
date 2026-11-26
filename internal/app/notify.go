package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/port"
)

var _ port.PaymentNotifier = (*Service)(nil)

// Completed applies a completion notice. It returns nil only after the commit,
// so the payment side resends the notice until then.
func (s *Service) Completed(ctx context.Context, c port.Completion) error {
	err := s.transition(ctx, c.PurchaseID, domain.EvPaymentCompleted, domain.Facts{CompletedAt: c.At}, &c)
	return s.notified(ctx, c.PurchaseID, err)
}

// Failed applies a failure notice; the payment_id is not stored for a failure.
func (s *Service) Failed(ctx context.Context, purchaseID, _ string) error {
	return s.notified(ctx, purchaseID, s.apply(ctx, purchaseID, domain.EvPaymentFailed, domain.Facts{}))
}

func (s *Service) notified(ctx context.Context, purchaseID string, err error) error {
	if !errors.Is(err, domain.ErrInvalidTransition) {
		return err
	}
	// An invalid transition never becomes valid, so an error would make the payment side resend it forever.
	s.Errors.Report(ctx, err, slog.String("purchase_id", purchaseID))
	return nil
}
