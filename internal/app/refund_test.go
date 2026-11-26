package app_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/port"
)

// funcPayment overrides Refund and Completed of the wrapped payment.
type funcPayment struct {
	port.Payment
	mu        sync.Mutex
	refunded  []string
	refundErr error
	failOnly  string // when set, only this payment_id gets refundErr
	completed func(since time.Time) ([]port.Completion, error)
}

func (p *funcPayment) Refund(_ context.Context, paymentID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refunded = append(p.refunded, paymentID)
	if p.failOnly != "" && p.failOnly != paymentID {
		return nil
	}
	return p.refundErr
}

func (p *funcPayment) Completed(_ context.Context, since time.Time) ([]port.Completion, error) {
	return p.completed(since)
}

type billingRow struct {
	PurchaseID, PaymentID, Kind string
	Amount, At                  int64
}

func (e *env) refunds() []billingRow {
	e.t.Helper()
	rows, err := e.s.Write.QueryContext(e.t.Context(),
		"SELECT purchase_id, payment_id, kind, amount, at FROM billing_logs WHERE kind = 'refund' ORDER BY purchase_id")
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []billingRow
	for rows.Next() {
		var b billingRow
		if err := rows.Scan(&b.PurchaseID, &b.PaymentID, &b.Kind, &b.Amount, &b.At); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) orphanState(paymentID string) string {
	e.t.Helper()
	var s string
	if err := e.s.Write.QueryRowContext(e.t.Context(), "SELECT state FROM orphan_refunds WHERE payment_id = ?", paymentID).Scan(&s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// refundingSetup leaves one refunding purchase and one refunding orphan row.
func refundingSetup(t *testing.T) (*env, *funcPayment, string, string) {
	t.Helper()
	e := newEnv(t, inventory.NewMemory())
	id, pay := e.heldPurchase("a@x", 2)
	if err := e.complete(id, pay, expiresAt, 2*price); err != nil {
		t.Fatal(err)
	}
	if err := e.complete("ghost", "pay-ghost", now, 777); err != nil {
		t.Fatal(err)
	}
	fp := &funcPayment{Payment: e.pay}
	e.deps.Payment = fp
	return e, fp, id, pay
}

func TestRetryRefundsRefundsPurchasesAndOrphans(t *testing.T) {
	e, fp, id, pay := refundingSetup(t)
	e.clock = now.Add(time.Hour)
	svc := e.svc()

	n, err := svc.RetryRefunds(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v; want 2, nil", n, err)
	}
	if !slices.Equal(fp.refunded, []string{pay, "pay-ghost"}) {
		t.Errorf("Refund calls = %v, want [%s pay-ghost]", fp.refunded, pay)
	}
	if e.stateRow(id).State != "refunded" || e.orphanState("pay-ghost") != "refunded" {
		t.Errorf("states = %s, %s; want refunded, refunded", e.stateRow(id).State, e.orphanState("pay-ghost"))
	}
	at := e.clock.UnixMilli()
	want := []billingRow{{id, pay, "refund", 2 * price, at}, {"ghost", "pay-ghost", "refund", 777, at}}
	slices.SortFunc(want, func(a, b billingRow) int { return strings.Compare(a.PurchaseID, b.PurchaseID) })
	if got := e.refunds(); !slices.Equal(got, want) {
		t.Errorf("refund billing = %+v, want %+v", got, want)
	}
	if got := e.allSales().Refunds; got != 1 {
		t.Errorf("event_sales refunds = %d, want 1", got)
	}
	wantNoRetry(t, svc, fp)
}

func wantNoRetry(t *testing.T, svc *app.Service, fp *funcPayment) {
	t.Helper()
	if n, err := svc.RetryRefunds(t.Context()); err != nil || n != 0 || len(fp.refunded) != 2 {
		t.Errorf("second run: n = %d, err = %v, calls = %d; want 0, nil, 2", n, err, len(fp.refunded))
	}
}

func TestRetryRefundsKeepsRowsWhenRefundFails(t *testing.T) {
	e, fp, id, _ := refundingSetup(t)
	fp.refundErr = errBoom

	n, err := e.svc().RetryRefunds(t.Context())
	if !errors.Is(err, errBoom) || n != 0 || len(fp.refunded) != 2 {
		t.Fatalf("n = %d, err = %v, calls = %d; want 0, boom, 2", n, err, len(fp.refunded))
	}
	if e.stateRow(id).State != "refunding" || e.orphanState("pay-ghost") != "refunding" || len(e.refunds()) != 0 {
		t.Errorf("states = %s, %s, refund rows = %d; want refunding, refunding, 0",
			e.stateRow(id).State, e.orphanState("pay-ghost"), len(e.refunds()))
	}
}

func TestRetryRefundsReturnsListError(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	_ = e.s.Read.Close()
	if n, err := e.svc().RetryRefunds(t.Context()); err == nil || n != 0 {
		t.Errorf("n = %d, err = %v; want 0 and an error", n, err)
	}
}

func TestRetryRefundsReturnsOrphanRefundError(t *testing.T) {
	e, fp, id, _ := refundingSetup(t)
	fp.refundErr, fp.failOnly = errBoom, "pay-ghost"
	n, err := e.svc().RetryRefunds(t.Context())
	if n != 1 || !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "refund orphan payment pay-ghost") {
		t.Fatalf("n = %d, err = %v; want 1 and the orphan's refund error", n, err)
	}
	if e.stateRow(id).State != "refunded" || e.orphanState("pay-ghost") != "refunding" {
		t.Errorf("states = %s, %s; want refunded, refunding", e.stateRow(id).State, e.orphanState("pay-ghost"))
	}
}
