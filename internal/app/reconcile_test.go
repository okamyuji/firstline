package app_test

import (
	"errors"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/port"
)

func TestReconcileAppliesCompletionsSince(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, pay := e.heldPurchase("a@x", 1)
	since := now.Add(-15 * time.Minute)
	var gotSince time.Time
	e.deps.Payment = &funcPayment{Payment: e.pay, completed: func(s time.Time) ([]port.Completion, error) {
		gotSince = s
		return []port.Completion{{PurchaseID: id, PaymentID: pay, Amount: price, At: now.Add(time.Minute)}}, nil
	}}
	svc := e.svc()
	if !svc.LastReconcileAt().IsZero() {
		t.Errorf("LastReconcileAt before any run = %v, want zero", svc.LastReconcileAt())
	}

	e.clock = now.Add(2 * time.Minute)
	n, err := svc.Reconcile(t.Context(), since)
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v; want 1, nil", n, err)
	}
	if !gotSince.Equal(since) {
		t.Errorf("Completed since = %v, want %v", gotSince, since)
	}
	if got := e.stateRow(id).State; got != "purchased" {
		t.Errorf("state = %s, want purchased", got)
	}
	if got := svc.LastReconcileAt(); !got.Equal(e.clock) {
		t.Errorf("LastReconcileAt = %v, want %v", got, e.clock)
	}
}

func TestReconcileKeepsLastRunTimeOnItemError(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	e.deps.Inventory = &releaseFailsOnce{Inventory: e.inv}
	a, payA := e.heldPurchase("a@x", 1)
	b, payB := e.heldPurchase("b@x", 1)
	// Late completions release seats, so the first one fails on Release.
	cs := []port.Completion{
		{PurchaseID: a, PaymentID: payA, Amount: price, At: expiresAt},
		{PurchaseID: b, PaymentID: payB, Amount: price, At: expiresAt},
	}
	e.deps.Payment = &funcPayment{Payment: e.pay, completed: func(time.Time) ([]port.Completion, error) { return cs, nil }}
	svc := e.svc()

	n, err := svc.Reconcile(t.Context(), now)
	if !errors.Is(err, errBoom) || n != 1 {
		t.Fatalf("n = %d, err = %v; want 1, boom", n, err)
	}
	if !svc.LastReconcileAt().IsZero() {
		t.Errorf("LastReconcileAt = %v, want zero after a failed item", svc.LastReconcileAt())
	}
	if e.stateRow(b).State != "refunding" {
		t.Errorf("second purchase state = %s, want refunding", e.stateRow(b).State)
	}
}

func TestReconcileReturnsCompletedError(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	e.deps.Payment = &funcPayment{Payment: e.pay, completed: func(time.Time) ([]port.Completion, error) { return nil, errBoom }}
	svc := e.svc()
	n, err := svc.Reconcile(t.Context(), now)
	if !errors.Is(err, errBoom) || n != 0 || !svc.LastReconcileAt().IsZero() {
		t.Errorf("n = %d, err = %v, last = %v; want 0, boom, zero", n, err, svc.LastReconcileAt())
	}
}
