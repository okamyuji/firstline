package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/uptrace/bun"
)

type refundingPurchase struct {
	ID        string `bun:"id"`
	PaymentID string `bun:"payment_id"`
	Amount    int    `bun:"amount"`
}

type refundingOrphan struct {
	PaymentID  string `bun:"payment_id"`
	PurchaseID string `bun:"purchase_id"`
	Amount     int    `bun:"amount"`
}

// RetryRefunds refunds every refunding purchase and orphan row. A Refund error
// leaves the row refunding for the next run.
func (s *Service) RetryRefunds(ctx context.Context) (int, error) {
	var ps []refundingPurchase
	if err := s.Store.Select(ctx, s.Store.Read, &ps, "list_refunding"); err != nil {
		return 0, fmt.Errorf("list refunding purchases: %w", err)
	}
	var orphans []refundingOrphan
	if err := s.Store.Select(ctx, s.Store.Read, &orphans, "list_refunding_orphans"); err != nil {
		return 0, fmt.Errorf("list refunding orphans: %w", err)
	}
	n, err := each(ps, func(p refundingPurchase) error {
		if err := s.Payment.Refund(ctx, p.PaymentID); err != nil {
			return fmt.Errorf("refund purchase %s: %w", p.ID, err)
		}
		return s.apply(ctx, p.ID, domain.EvRefundSucceeded, domain.Facts{})
	})
	m, oerr := each(orphans, func(o refundingOrphan) error {
		if err := s.Payment.Refund(ctx, o.PaymentID); err != nil {
			return fmt.Errorf("refund orphan payment %s: %w", o.PaymentID, err)
		}
		return s.orphanRefunded(ctx, o)
	})
	return n + m, errors.Join(err, oerr)
}

func (s *Service) orphanRefunded(ctx context.Context, o refundingOrphan) error {
	return s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) error {
		now := s.Clock().UnixMilli()
		n, err := affected(s.Store.Exec(ctx, tx, "set_orphan_refunded", now, o.PaymentID))
		if err != nil || n == 0 {
			return err
		}
		_, err = s.Store.Exec(ctx, tx, "insert_billing", o.PurchaseID, o.PaymentID, "refund", o.Amount, now)
		return err
	})
}
