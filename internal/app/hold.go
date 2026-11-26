package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/uptrace/bun"
)

const maxQuantity = 4

type HoldInput struct {
	EventID, Email, Lang string
	Quantity             int
}

type HoldResult struct {
	PurchaseID, CheckoutURL string
	ExpiresAt               time.Time
}

// bun rejects a column the struct lacks, so these mirror every column the queries return.
type eventRow struct {
	ID               string `bun:"id"`
	OrganizerID      string `bun:"organizer_id"`
	TitleJA          string `bun:"title_ja"`
	TitleEN          string `bun:"title_en"`
	StartsAt         int64  `bun:"starts_at"`
	EndsAt           int64  `bun:"ends_at"`
	Capacity         int    `bun:"capacity"`
	Price            int    `bun:"price"`
	SaleStartsAt     int64  `bun:"sale_starts_at"`
	NextTicketNumber int    `bun:"next_ticket_number"`
}

type purchaseRow struct {
	ID               string         `bun:"id"`
	EventID          string         `bun:"event_id"`
	Email            string         `bun:"email"`
	Lang             string         `bun:"lang"`
	Quantity         int            `bun:"quantity"`
	Amount           int            `bun:"amount"`
	State            string         `bun:"state"`
	ExpiresAt        int64          `bun:"expires_at"`
	PaymentID        sql.NullString `bun:"payment_id"`
	CheckoutURL      sql.NullString `bun:"checkout_url"`
	TicketNumberFrom sql.NullInt64  `bun:"ticket_number_from"`
	UpdatedAt        int64          `bun:"updated_at"`
}

type newHold struct {
	id, eventID, email, lang string
	quantity                 int
	now, expiresAt           time.Time
}

// Hold reserves seats, commits a held purchase, starts the payment and records
// its payment_id (steps 1 to 4 of the purchase flow).
func (s *Service) Hold(ctx context.Context, in HoldInput) (HoldResult, error) {
	if in.Quantity < 1 || in.Quantity > maxQuantity {
		return HoldResult{}, domain.ErrBadQuantity
	}
	if err := s.Inventory.Reserve(ctx, in.EventID, in.Quantity); err != nil {
		return HoldResult{}, err
	}
	now := s.Clock()
	h := newHold{
		id: s.NewID(), eventID: in.EventID, email: strings.ToLower(strings.TrimSpace(in.Email)), lang: in.Lang,
		quantity: in.Quantity, now: now, expiresAt: now.Add(domain.HoldTTL),
	}
	var amount int
	err := s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) (err error) {
		amount, err = s.writeHold(ctx, tx, h)
		return err
	})
	if err != nil {
		return HoldResult{}, errors.Join(err, s.Inventory.Release(ctx, h.eventID, h.quantity))
	}

	paymentID, checkoutURL, err := s.Payment.Start(ctx, h.id, amount)
	if err != nil {
		return HoldResult{}, s.startFailed(ctx, h, err)
	}
	// The completion notice writes payment_id when this item fails, so the purchase stays held.
	err = s.Writer.Do(context.WithoutCancel(ctx), func(ctx context.Context, tx bun.Tx) error {
		return s.recordPayment(ctx, tx, h.id, paymentID, checkoutURL)
	})
	if err != nil {
		s.Errors.Report(ctx, err, slog.String("purchase_id", h.id))
	}
	return HoldResult{PurchaseID: h.id, CheckoutURL: checkoutURL, ExpiresAt: h.expiresAt}, nil
}

func (s *Service) writeHold(ctx context.Context, tx bun.Tx, h newHold) (int, error) {
	ev, err := s.event(ctx, tx, h.eventID)
	if err != nil {
		return 0, err
	}
	if err := s.checkHold(ctx, tx, h, ev); err != nil {
		return 0, err
	}
	n, err := affected(s.Store.Exec(ctx, tx, "hold_seats", h.quantity, h.quantity, h.eventID, h.quantity))
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, domain.ErrSoldOut
	}
	amount := ev.Price * h.quantity
	if _, err := s.Store.Exec(ctx, tx, "insert_purchase", h.id, h.eventID, h.email, h.lang, h.quantity, amount,
		h.expiresAt.UnixMilli(), h.now.UnixMilli()); err != nil {
		return 0, err
	}
	if _, err := s.Store.Exec(ctx, tx, "insert_mail", h.id, string(domain.MailHold), h.lang); err != nil {
		return 0, err
	}
	return amount, nil
}

func (s *Service) checkHold(ctx context.Context, tx bun.Tx, h newHold, ev eventRow) error {
	var active int
	if err := s.Store.Select(ctx, tx, &active, "count_active_holds", h.eventID, h.email); err != nil {
		return err
	}
	if active > 0 {
		return domain.ErrDuplicateHold
	}
	if at := h.now.UnixMilli(); at < ev.SaleStartsAt || at >= ev.StartsAt {
		return domain.ErrNotOnSale
	}
	return nil
}

func (s *Service) event(ctx context.Context, tx bun.Tx, id string) (eventRow, error) {
	var ev eventRow
	err := s.Store.Select(ctx, tx, &ev, "get_event", id)
	if errors.Is(err, sql.ErrNoRows) {
		err = domain.ErrNotFound
	}
	return ev, err
}

// startFailed releases the hold (step 4). The seats go back to the inventory
// only after the release commits, so a failed commit cannot return them twice.
func (s *Service) startFailed(ctx context.Context, h newHold, startErr error) error {
	err := fmt.Errorf("%w: %w", domain.ErrPaymentUnavailable, startErr)
	ctx = context.WithoutCancel(ctx)
	werr := s.Writer.Do(ctx, func(ctx context.Context, tx bun.Tx) error {
		return s.releaseHold(ctx, tx, h)
	})
	if werr != nil {
		return errors.Join(err, werr)
	}
	return errors.Join(err, s.Inventory.Release(ctx, h.eventID, h.quantity))
}

func (s *Service) releaseHold(ctx context.Context, tx bun.Tx, h newHold) error {
	d, err := domain.Next(domain.StateHeld, domain.EvStartFailed, domain.Facts{})
	if err != nil {
		return err
	}
	n, err := affected(s.Store.Exec(ctx, tx, "set_state", string(d.To), s.Clock().UnixMilli(), h.id, string(domain.StateHeld)))
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("release purchase %s: %w", h.id, domain.ErrInvalidTransition)
	}
	_, err = s.Store.Exec(ctx, tx, "release_seats", h.quantity, h.quantity, h.eventID, h.quantity)
	return err
}

// recordPayment writes payment_id only when it is still empty. An equal stored
// value means a completion notice got there first.
func (s *Service) recordPayment(ctx context.Context, tx bun.Tx, purchaseID, paymentID, checkoutURL string) error {
	n, err := affected(s.Store.Exec(ctx, tx, "record_payment", paymentID, checkoutURL, purchaseID))
	if err != nil || n > 0 {
		return err
	}
	var p purchaseRow
	if err := s.Store.Select(ctx, tx, &p, "get_purchase", purchaseID); err != nil {
		return err
	}
	if p.PaymentID.String != paymentID {
		return fmt.Errorf("purchase %s has payment_id %q, Start returned %q", purchaseID, p.PaymentID.String, paymentID)
	}
	return nil
}

func affected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
