package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/port"
)

// releaseFailsOnce reports an error on its first Release only, after returning the seats.
type releaseFailsOnce struct {
	port.Inventory
	calls atomic.Int32
}

func (r *releaseFailsOnce) Release(ctx context.Context, id string, q int) error {
	err := r.Inventory.Release(ctx, id, q)
	if r.calls.Add(1) == 1 {
		return errors.Join(err, errBoom)
	}
	return err
}

func TestExpireDueExpiresHoldsAtTheGraceBoundary(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	id, _ := e.heldPurchase("a@x", 2)
	svc := e.svc()

	expire := func(step string, want int, wantState string) {
		t.Helper()
		n, err := svc.ExpireDue(t.Context())
		if err != nil || n != want {
			t.Fatalf("%s: n = %d, err = %v; want %d, nil", step, n, err, want)
		}
		if got := e.stateRow(id).State; got != wantState {
			t.Fatalf("%s: state = %s, want %s", step, got, wantState)
		}
	}

	e.clock = expiresAt.Add(domain.ExpiryGrace - time.Millisecond)
	expire("1ms early", 0, "held")
	e.clock = expiresAt.Add(domain.ExpiryGrace)
	expire("at grace", 1, "expired")
	if held, remaining := e.sales(); held != 0 || remaining != capacity {
		t.Errorf("event_sales held = %d, remaining = %d; want 0, %d", held, remaining, capacity)
	}
	e.wantRemaining(capacity)
	expire("second run", 0, "expired")
}

func TestExpireDueContinuesPastItemErrors(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	e.deps.Inventory = &releaseFailsOnce{Inventory: e.inv}
	a, _ := e.heldPurchase("a@x", 1)
	b, _ := e.heldPurchase("b@x", 1)

	e.clock = expiresAt.Add(domain.ExpiryGrace)
	n, err := e.svc().ExpireDue(t.Context())
	if !errors.Is(err, errBoom) || n != 1 {
		t.Fatalf("n = %d, err = %v; want 1, boom", n, err)
	}
	if e.stateRow(a).State != "expired" || e.stateRow(b).State != "expired" {
		t.Errorf("states = %s, %s; want both expired", e.stateRow(a).State, e.stateRow(b).State)
	}
}

func TestExpireDueReturnsListError(t *testing.T) {
	e := newEnv(t, inventory.NewMemory())
	_ = e.s.Read.Close()
	if n, err := e.svc().ExpireDue(t.Context()); err == nil || n != 0 {
		t.Errorf("n = %d, err = %v; want 0 and an error", n, err)
	}
}
