package app

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/uptrace/bun"
)

type applied struct {
	p purchaseRow
	d domain.Decision
}

// apply writes one transition of the purchase as a single sync item.
func (s *Service) apply(ctx context.Context, purchaseID string, ev domain.Event, f domain.Facts) error {
	return s.transition(ctx, purchaseID, ev, f, nil)
}

// transition is apply with the completion notice c, nil for other events.
// Seats go back to the inventory only after the commit, so a failed commit
// cannot return them twice.
func (s *Service) transition(ctx context.Context, purchaseID string, ev domain.Event, f domain.Facts, c *port.Completion) error {
	var r applied
	err := s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) (err error) {
		r, err = s.writeTransition(ctx, tx, purchaseID, ev, f, c)
		return err
	})
	if err != nil || !r.d.Changed {
		return err
	}
	if c != nil && c.Amount != r.p.Amount {
		s.Errors.Report(ctx, fmt.Errorf("purchase %s: completion amount %d, purchase amount %d", purchaseID, c.Amount, r.p.Amount),
			slog.String("purchase_id", purchaseID))
	}
	if !r.d.ReleaseSeat {
		return nil
	}
	return s.Inventory.Release(context.WithoutCancel(ctx), r.p.EventID, r.p.Quantity)
}

func (s *Service) writeTransition(ctx context.Context, tx bun.Tx, id string, ev domain.Event, f domain.Facts, c *port.Completion) (applied, error) {
	now := s.Clock()
	f.Now = now
	var p purchaseRow
	err := s.Store.Select(ctx, tx, &p, "get_purchase", id)
	if errors.Is(err, sql.ErrNoRows) {
		return applied{}, s.noPurchase(ctx, tx, id, ev, c)
	}
	if err != nil {
		return applied{}, err
	}
	f.ExpiresAt = time.UnixMilli(p.ExpiresAt)
	d, err := domain.Next(domain.State(p.State), ev, f)
	if err != nil || !d.Changed {
		return applied{p, d}, err
	}
	n, err := affected(s.Store.Exec(ctx, tx, "set_state", string(d.To), now.UnixMilli(), id, p.State))
	if err != nil {
		return applied{}, err
	}
	// The read and this update run in one item on the single writer goroutine,
	// so no other transition can move the state in between; 0 rows is a defect.
	if n == 0 {
		return applied{}, fmt.Errorf("purchase %s: state %s changed under the writer: %w", id, p.State, domain.ErrInvalidTransition)
	}
	return applied{p, d}, s.writeEffects(ctx, tx, p, d, c, now.UnixMilli())
}

// noPurchase handles a notice for a purchase with no record: a completion
// becomes an orphan refund, a failure needs nothing.
func (s *Service) noPurchase(ctx context.Context, tx bun.Tx, id string, ev domain.Event, c *port.Completion) error {
	if c != nil {
		_, err := s.Store.Exec(ctx, tx, "insert_orphan", c.PaymentID, id, c.Amount, s.Clock().UnixMilli())
		return err
	}
	if ev == domain.EvPaymentFailed {
		return nil
	}
	return fmt.Errorf("purchase %s: %w", id, domain.ErrNotFound)
}

func (s *Service) writeEffects(ctx context.Context, tx bun.Tx, p purchaseRow, d domain.Decision, c *port.Completion, now int64) error {
	paymentID := p.PaymentID.String
	if c != nil {
		// record_payment writes only when payment_id is still empty.
		if _, err := s.Store.Exec(ctx, tx, "record_payment", c.PaymentID, p.CheckoutURL, p.ID); err != nil {
			return err
		}
		paymentID = cmp.Or(paymentID, c.PaymentID)
	}
	if d.ReleaseSeat {
		if err := s.moveSeats(ctx, tx, "release_seats", p); err != nil {
			return err
		}
	}
	if d.SellSeat {
		if err := s.sell(ctx, tx, p, now); err != nil {
			return err
		}
	}
	if err := s.writeBilling(ctx, tx, p, d, paymentID, now); err != nil {
		return err
	}
	if d.Mail == domain.MailNone {
		return nil
	}
	_, err := s.Store.Exec(ctx, tx, "insert_mail", p.ID, string(d.Mail), p.Lang)
	return err
}

func (s *Service) writeBilling(ctx context.Context, tx bun.Tx, p purchaseRow, d domain.Decision, paymentID string, now int64) error {
	if d.Charge {
		_, err := s.Store.Exec(ctx, tx, "insert_billing", p.ID, paymentID, "charge", p.Amount, now)
		return err
	}
	if !d.Refunded {
		return nil
	}
	if _, err := s.Store.Exec(ctx, tx, "insert_billing", p.ID, paymentID, "refund", p.Amount, now); err != nil {
		return err
	}
	_, err := s.Store.Exec(ctx, tx, "count_refund", p.EventID)
	return err
}

// moveSeats runs release_seats or sell_seats; no row changed means the event
// holds fewer seats than the purchase, which the item must not commit.
func (s *Service) moveSeats(ctx context.Context, tx bun.Tx, name string, p purchaseRow) error {
	n, err := affected(s.Store.Exec(ctx, tx, name, p.Quantity, p.Quantity, p.EventID, p.Quantity))
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s for purchase %s: fewer than %d seats held", name, p.ID, p.Quantity)
	}
	return nil
}

func (s *Service) sell(ctx context.Context, tx bun.Tx, p purchaseRow, now int64) error {
	if err := s.moveSeats(ctx, tx, "sell_seats", p); err != nil {
		return err
	}
	if _, err := s.Store.Exec(ctx, tx, "advance_ticket_number", p.Quantity, p.EventID); err != nil {
		return err
	}
	var base int64
	if err := s.Store.Select(ctx, tx, &base, "get_ticket_base", p.Quantity, p.EventID); err != nil {
		return err
	}
	_, err := s.Store.Exec(ctx, tx, "assign_ticket", base, now, p.ID)
	return err
}
