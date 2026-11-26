package app_test

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/port"
)

var expiresAt = now.Add(domain.HoldTTL)

// heldPurchase creates a held purchase at now and returns its ID and payment_id.
func (e *env) heldPurchase(email string, qty int) (string, string) {
	e.t.Helper()
	res, err := e.hold(email, qty)
	if err != nil {
		e.t.Fatal(err)
	}
	return res.PurchaseID, *e.purchase(res.PurchaseID).PaymentID
}

func (e *env) complete(id, paymentID string, at time.Time, amount int) error {
	return e.svc().Completed(e.t.Context(), port.Completion{PurchaseID: id, PaymentID: paymentID, Amount: amount, At: at})
}

type stateRow struct {
	State       string
	Ticket      sql.NullInt64
	UpdatedAt   int64
	PaymentID   sql.NullString
	CheckoutURL sql.NullString
}

func (e *env) stateRow(id string) stateRow {
	e.t.Helper()
	var r stateRow
	if err := e.s.Write.QueryRowContext(e.t.Context(),
		"SELECT state, ticket_number_from, updated_at, payment_id, checkout_url FROM purchases WHERE id = ?", id).
		Scan(&r.State, &r.Ticket, &r.UpdatedAt, &r.PaymentID, &r.CheckoutURL); err != nil {
		e.t.Fatal(err)
	}
	return r
}

type salesRow struct{ Held, Sold, Remaining, Refunds int }

func (e *env) allSales() salesRow {
	e.t.Helper()
	var r salesRow
	if err := e.s.Write.QueryRowContext(e.t.Context(),
		"SELECT held, sold, remaining, refunds FROM event_sales WHERE event_id = 'e1'").
		Scan(&r.Held, &r.Sold, &r.Remaining, &r.Refunds); err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.s.Write.ExecContext(e.t.Context(), q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) wantSales(want salesRow) {
	e.t.Helper()
	if got := e.allSales(); got != want {
		e.t.Errorf("event_sales = %+v, want %+v", got, want)
	}
}

func (e *env) wantCount(want int, q string, args ...any) {
	e.t.Helper()
	if got := e.count(q, args...); got != want {
		e.t.Errorf("%s = %d, want %d", q, got, want)
	}
}

func (e *env) wantReports(want int) {
	e.t.Helper()
	if got := e.errs.count(); got != want {
		e.t.Errorf("reports = %d (%v), want %d", got, e.errs.errs, want)
	}
}

func TestCompletedBeforeExpiryPurchases(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 2)
	e.clock = now.Add(time.Minute)
	if err := e.complete(id, pid, expiresAt.Add(-time.Millisecond), 2*price); err != nil {
		t.Fatal(err)
	}
	r := e.stateRow(id)
	if r.State != "purchased" || r.Ticket.Int64 != 1 || r.UpdatedAt != e.clock.UnixMilli() {
		t.Errorf("purchase = %+v, want purchased with ticket 1 at %d", r, e.clock.UnixMilli())
	}
	e.wantCount(3, "SELECT next_ticket_number FROM events WHERE id = 'e1'")
	e.wantSales(salesRow{Held: 0, Sold: 2, Remaining: capacity - 2})
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs WHERE purchase_id = ? AND payment_id = ? AND kind = 'charge' AND amount = ? AND at = ?",
		id, pid, 2*price, e.clock.UnixMilli())
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs")
	e.wantCount(1, "SELECT COUNT(*) FROM mail_outbox WHERE purchase_id = ? AND kind = 'purchased' AND lang = 'en'", id)
	e.wantRemaining(capacity - 2)
	e.wantReports(0)

	id2, pid2 := e.heldPurchase("b@x", 1)
	if err := e.complete(id2, pid2, now.Add(time.Minute), price); err != nil {
		t.Fatal(err)
	}
	if r := e.stateRow(id2); r.Ticket.Int64 != 3 {
		t.Errorf("second ticket = %v, want 3", r.Ticket.Int64)
	}
}

func TestCompletedAtExpiryRefunds(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 2)
	if err := e.complete(id, pid, expiresAt, 2*price); err != nil {
		t.Fatal(err)
	}
	if r := e.stateRow(id); r.State != "refunding" || r.Ticket.Valid {
		t.Errorf("purchase = %+v, want refunding without a ticket", r)
	}
	e.wantSales(salesRow{Remaining: capacity})
	e.wantCount(0, "SELECT COUNT(*) FROM billing_logs")
	e.wantCount(1, "SELECT COUNT(*) FROM mail_outbox WHERE purchase_id = ? AND kind = 'refund'", id)
	e.wantRemaining(capacity)
	e.wantReports(0)
}

func TestCompletedTwiceChangesNothing(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 2)
	if err := e.complete(id, pid, now, 2*price); err != nil {
		t.Fatal(err)
	}
	before, sales := e.stateRow(id), e.allSales()
	e.clock = now.Add(time.Minute)
	if err := e.complete(id, pid, now, 2*price); err != nil {
		t.Fatalf("second completion: %v", err)
	}
	if after := e.stateRow(id); after != before {
		t.Errorf("purchase changed: %+v -> %+v", before, after)
	}
	e.wantSales(sales)
	e.wantCount(3, "SELECT next_ticket_number FROM events WHERE id = 'e1'")
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs")
	e.wantCount(1, "SELECT COUNT(*) FROM mail_outbox WHERE kind = 'purchased'")
	e.wantRemaining(capacity - 2)
	e.wantReports(0)
}

func TestFailedThenCompletedReturnsSeatsOnce(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 2)
	e.clock = now.Add(time.Minute)
	if err := e.svc().Failed(t.Context(), id, pid); err != nil {
		t.Fatal(err)
	}
	if r := e.stateRow(id); r.State != "released" || r.UpdatedAt != e.clock.UnixMilli() {
		t.Errorf("purchase = %+v, want released", r)
	}
	e.wantSales(salesRow{Remaining: capacity})
	e.wantRemaining(capacity)
	if err := e.svc().Failed(t.Context(), id, pid); err != nil {
		t.Fatalf("second failure: %v", err)
	}
	if err := e.complete(id, pid, now.Add(time.Minute), 2*price); err != nil {
		t.Fatal(err)
	}
	if r := e.stateRow(id); r.State != "refunding" {
		t.Errorf("state = %q, want refunding", r.State)
	}
	e.wantSales(salesRow{Remaining: capacity})
	e.wantRemaining(capacity)
	e.wantCount(0, "SELECT COUNT(*) FROM billing_logs")
	e.wantCount(1, "SELECT COUNT(*) FROM mail_outbox WHERE purchase_id = ? AND kind = 'refund'", id)
	e.wantReports(0)
}

func TestCompletedWritesMissingPaymentID(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, _ := e.heldPurchase("a@x", 1)
	e.exec("UPDATE purchases SET payment_id = NULL, checkout_url = 'https://pay/x' WHERE id = ?", id)
	if err := e.complete(id, "pay-late", now, price); err != nil {
		t.Fatal(err)
	}
	r := e.stateRow(id)
	if r.State != "purchased" || r.PaymentID.String != "pay-late" || r.CheckoutURL.String != "https://pay/x" {
		t.Errorf("purchase = %+v, want purchased with payment_id pay-late and the checkout URL kept", r)
	}
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs WHERE payment_id = 'pay-late'")
}

// refundingPaths are the rows of the transition table where a completion moves
// the purchase to refunding. Each leaves a 2-seat purchase ready for that
// completion and returns when the payment completed.
var refundingPaths = map[string]func(e *env, id, pid string) time.Time{
	"late": func(*env, string, string) time.Time { return expiresAt },
	"expired": func(e *env, id, _ string) time.Time {
		e.clock = expiresAt.Add(domain.ExpiryGrace)
		if err := e.svc().Apply(e.t.Context(), id, domain.EvExpire, domain.Facts{}); err != nil {
			e.t.Fatal(err)
		}
		return now
	},
	"released": func(e *env, id, pid string) time.Time {
		if err := e.svc().Failed(e.t.Context(), id, pid); err != nil {
			e.t.Fatal(err)
		}
		return now
	},
}

// A purchase whose Start record failed has no payment_id; the refund retry
// selects only rows with one, so the completion must write it on these rows too.
func TestCompletionIntoRefundingWritesMissingPaymentIDAndIsRefunded(t *testing.T) {
	for name, path := range refundingPaths {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, inventory.NewMemory())
			id, pid := e.heldPurchase("a@x", 2)
			at := path(e, id, pid)
			e.exec("UPDATE purchases SET payment_id = NULL WHERE id = ?", id)
			if err := e.complete(id, "pay-late", at, 2*price); err != nil {
				t.Fatal(err)
			}
			if r := e.stateRow(id); r.State != "refunding" || r.PaymentID.String != "pay-late" {
				t.Fatalf("purchase = %+v, want refunding with payment_id pay-late", r)
			}
			fp := &funcPayment{Payment: e.pay}
			e.deps.Payment = fp
			if n, err := e.svc().RetryRefunds(t.Context()); n != 1 || err != nil || !slices.Equal(fp.refunded, []string{"pay-late"}) {
				t.Errorf("RetryRefunds = %d, %v, refunded %v; want 1, nil, [pay-late]", n, err, fp.refunded)
			}
			if r := e.stateRow(id); r.State != "refunded" {
				t.Errorf("state = %q, want refunded", r.State)
			}
		})
	}
}

func TestCompletionIntoRefundingReportsAmountMismatch(t *testing.T) {
	for name, path := range refundingPaths {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, inventory.NewMemory())
			id, pid := e.heldPurchase("a@x", 2)
			if err := e.complete(id, pid, path(e, id, pid), 2*price+1); err != nil {
				t.Fatal(err)
			}
			if r := e.stateRow(id); r.State != "refunding" {
				t.Errorf("state = %q, want refunding", r.State)
			}
			e.wantReports(1)
		})
	}
}

func TestCompletedKeepsStoredPaymentID(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 1)
	if err := e.complete(id, "pay-other", now, price); err != nil {
		t.Fatal(err)
	}
	if r := e.stateRow(id); r.PaymentID.String != pid {
		t.Errorf("payment_id = %q, want %q", r.PaymentID.String, pid)
	}
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs WHERE payment_id = ?", pid)
}

func TestCompletedWithoutPurchaseRecordsOrphanOnce(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	for range 2 {
		if err := e.complete("ghost", "pay-o", now, 7000); err != nil {
			t.Fatal(err)
		}
	}
	e.wantCount(1, "SELECT COUNT(*) FROM orphan_refunds")
	e.wantCount(1, "SELECT COUNT(*) FROM orphan_refunds WHERE payment_id = 'pay-o' AND purchase_id = 'ghost' AND amount = 7000 AND state = 'refunding' AND updated_at = ?",
		now.UnixMilli())
	if err := e.svc().Failed(t.Context(), "ghost", "pay-o"); err != nil {
		t.Errorf("failure without a purchase: %v", err)
	}
	if err := e.svc().Apply(t.Context(), "ghost", domain.EvExpire, domain.Facts{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expiry without a purchase: err = %v, want ErrNotFound", err)
	}
	e.wantReports(0)
}

func TestCompletedAmountMismatchIsReported(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 2)
	if err := e.complete(id, pid, now, 2*price+1); err != nil {
		t.Fatal(err)
	}
	e.wantReports(1)
	if r := e.stateRow(id); r.State != "purchased" {
		t.Errorf("state = %q, want purchased", r.State)
	}
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs WHERE amount = ?", 2*price)
}

func TestNotificationOnUnknownStateIsReportedNotRetried(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 1)
	e.exec("UPDATE purchases SET state = 'bogus' WHERE id = ?", id)
	if err := e.complete(id, pid, now, price); err != nil {
		t.Errorf("completion: %v", err)
	}
	if err := e.svc().Failed(t.Context(), id, pid); err != nil {
		t.Errorf("failure: %v", err)
	}
	e.wantReports(2)
	if err := e.svc().Apply(t.Context(), id, domain.EvExpire, domain.Facts{}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("expiry: err = %v, want ErrInvalidTransition", err)
	}
}

func TestApplyFailsWhenSeatsAreNotHeld(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 2)
	e.exec("UPDATE event_sales SET held = 1")
	if err := e.svc().Failed(t.Context(), id, pid); err == nil {
		t.Error("release with too few held seats: err = nil")
	}
	if err := e.complete(id, pid, now, 2*price); err == nil {
		t.Error("sale with too few held seats: err = nil")
	}
	if r := e.stateRow(id); r.State != "held" {
		t.Errorf("state = %q, want held after the rollback", r.State)
	}
	e.wantRemaining(capacity - 2)
}

func TestExpireThenRefundSucceeded(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pid := e.heldPurchase("a@x", 2)
	e.clock = expiresAt.Add(domain.ExpiryGrace)
	if err := e.svc().Apply(t.Context(), id, domain.EvExpire, domain.Facts{}); err != nil {
		t.Fatal(err)
	}
	if r := e.stateRow(id); r.State != "expired" {
		t.Errorf("state = %q, want expired", r.State)
	}
	e.wantSales(salesRow{Remaining: capacity})
	e.wantRemaining(capacity)
	e.wantCount(0, "SELECT COUNT(*) FROM mail_outbox WHERE kind != 'hold'")

	if err := e.complete(id, pid, now, 2*price); err != nil {
		t.Fatal(err)
	}
	if err := e.svc().Apply(t.Context(), id, domain.EvRefundSucceeded, domain.Facts{}); err != nil {
		t.Fatal(err)
	}
	if r := e.stateRow(id); r.State != "refunded" {
		t.Errorf("state = %q, want refunded", r.State)
	}
	e.wantSales(salesRow{Remaining: capacity, Refunds: 1})
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs WHERE purchase_id = ? AND payment_id = ? AND kind = 'refund' AND amount = ? AND at = ?",
		id, pid, 2*price, e.clock.UnixMilli())
	e.wantCount(1, "SELECT COUNT(*) FROM billing_logs")
	e.wantRemaining(capacity)
}

func TestCompletionRacesExpiry(t *testing.T) {
	const qty = 2
	outcomes := map[string]int{}
	for i := range 100 {
		e := newEnv(t, inventory.NewMemory())
		id, pid := e.heldPurchase(fmt.Sprintf("a%d@x", i), qty)
		e.clock = expiresAt.Add(domain.ExpiryGrace)
		var wg sync.WaitGroup
		var completeErr, expireErr error
		wg.Go(func() { completeErr = e.complete(id, pid, expiresAt.Add(-time.Second), qty*price) })
		wg.Go(func() { expireErr = e.svc().Apply(t.Context(), id, domain.EvExpire, domain.Facts{}) })
		wg.Wait()
		if completeErr != nil {
			t.Fatalf("run %d: completion: %v", i, completeErr)
		}
		state := e.stateRow(id).State
		outcomes[state]++
		switch state {
		case "purchased":
			if !errors.Is(expireErr, domain.ErrInvalidTransition) {
				t.Fatalf("run %d: expiry after the sale: err = %v", i, expireErr)
			}
			e.wantSales(salesRow{Sold: qty, Remaining: capacity - qty})
			e.wantRemaining(capacity - qty)
		case "refunding":
			if expireErr != nil {
				t.Fatalf("run %d: expiry: %v", i, expireErr)
			}
			e.wantSales(salesRow{Remaining: capacity})
			e.wantRemaining(capacity)
		default:
			t.Fatalf("run %d: state = %q, want purchased or refunding", i, state)
		}
		if t.Failed() {
			t.Fatalf("run %d failed", i)
		}
	}
	t.Logf("outcomes: %v", outcomes)
}
