package inventory_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/okamyuji/firstline/internal/domain"
	"github.com/okamyuji/firstline/internal/inventory"
)

func remaining(t *testing.T, inv interface {
	Remaining(ctx context.Context, eventID string) (int, error)
}, id string) int {
	t.Helper()
	n, err := inv.Remaining(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMemoryReserveAndRelease(t *testing.T) {
	ctx := t.Context()
	m := inventory.NewMemory()
	if err := m.Add(ctx, "e", 5); err != nil {
		t.Fatal(err)
	}
	if err := m.Reserve(ctx, "e", 3); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, m, "e"); got != 2 {
		t.Fatalf("remaining = %d, want 2", got)
	}
	if err := m.Reserve(ctx, "e", 3); !errors.Is(err, domain.ErrSoldOut) {
		t.Fatalf("over-reserve err = %v, want ErrSoldOut", err)
	}
	if got := remaining(t, m, "e"); got != 2 {
		t.Fatalf("failed reserve changed remaining to %d", got)
	}
	if err := m.Reserve(ctx, "e", 2); err != nil {
		t.Fatalf("reserve exactly remaining: %v", err)
	}
	if err := m.Release(ctx, "e", 4); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, m, "e"); got != 4 {
		t.Fatalf("remaining = %d, want 4", got)
	}
}

func TestMemoryReleaseCapsAtCapacity(t *testing.T) {
	ctx := t.Context()
	m := inventory.NewMemory()
	_ = m.Add(ctx, "e", 5)
	_ = m.Reserve(ctx, "e", 2)
	_ = m.Release(ctx, "e", 2)
	_ = m.Release(ctx, "e", 2)
	if got := remaining(t, m, "e"); got != 5 {
		t.Fatalf("remaining = %d, want 5", got)
	}
}

func TestMemoryAddReplaces(t *testing.T) {
	ctx := t.Context()
	m := inventory.NewMemory()
	_ = m.Add(ctx, "e", 5)
	_ = m.Reserve(ctx, "e", 5)
	_ = m.Add(ctx, "e", 7)
	if got := remaining(t, m, "e"); got != 7 {
		t.Fatalf("remaining = %d, want 7", got)
	}
	_ = m.Release(ctx, "e", 100)
	if got := remaining(t, m, "e"); got != 7 {
		t.Fatalf("capacity after replace = %d, want 7", got)
	}
}

func TestMemoryErrors(t *testing.T) {
	ctx := t.Context()
	m := inventory.NewMemory()
	_ = m.Add(ctx, "e", 5)
	for _, q := range []int{0, -1} {
		if err := m.Reserve(ctx, "e", q); !errors.Is(err, domain.ErrBadQuantity) {
			t.Errorf("Reserve(%d) = %v, want ErrBadQuantity", q, err)
		}
		if err := m.Release(ctx, "e", q); !errors.Is(err, domain.ErrBadQuantity) {
			t.Errorf("Release(%d) = %v, want ErrBadQuantity", q, err)
		}
	}
	if got := remaining(t, m, "e"); got != 5 {
		t.Errorf("bad quantity changed remaining to %d", got)
	}
	if err := m.Reserve(ctx, "x", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Reserve unknown = %v", err)
	}
	if err := m.Release(ctx, "x", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Release unknown = %v", err)
	}
	if _, err := m.Remaining(ctx, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Remaining unknown = %v", err)
	}
}

func TestMemoryConcurrentReserve(t *testing.T) {
	ctx := t.Context()
	m := inventory.NewMemory()
	_ = m.Add(ctx, "e", 10)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if m.Reserve(ctx, "e", 1) == nil {
				ok.Add(1)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 10 {
		t.Fatalf("successes = %d, want 10", ok.Load())
	}
}

func TestDBIsNoop(t *testing.T) {
	ctx := t.Context()
	d := inventory.NewDB()
	if err := d.Add(ctx, "e", 5); err != nil {
		t.Fatal(err)
	}
	if err := d.Reserve(ctx, "e", 1); err != nil {
		t.Fatal(err)
	}
	if err := d.Release(ctx, "e", 1); err != nil {
		t.Fatal(err)
	}
	if n, err := d.Remaining(ctx, "e"); err != nil || n != 0 {
		t.Fatalf("Remaining = %d, %v", n, err)
	}
}
