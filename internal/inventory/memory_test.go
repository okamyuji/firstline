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

func TestMemoryReleaseRestoresSeatsAfterRebuild(t *testing.T) {
	ctx := t.Context()
	m := inventory.NewMemory()
	_ = m.Add(ctx, "e", 8) // capacity 10 with 2 held at rebuild
	if err := m.Release(ctx, "e", 2); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, m, "e"); got != 10 {
		t.Fatalf("remaining = %d, want 10", got)
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

// Reserve(2) against 11 seats leaves one seat that no request fits, so a
// check and a decrement that are not atomic together show as an oversell. A
// closed channel releases all goroutines at once to make that interleaving likely.
func TestMemoryConcurrentReserve(t *testing.T) {
	ctx := t.Context()
	for run := range 5000 {
		m := inventory.NewMemory()
		_ = m.Add(ctx, "e", 11)
		var ok atomic.Int32
		var wg sync.WaitGroup
		begin := make(chan struct{})
		for range 64 {
			wg.Go(func() {
				<-begin
				if m.Reserve(ctx, "e", 2) == nil {
					ok.Add(1)
				}
			})
		}
		close(begin)
		wg.Wait()
		left, _ := m.Remaining(ctx, "e")
		if ok.Load() != 5 || left != 1 {
			t.Fatalf("run %d: successes = %d, remaining = %d; want 5 and 1", run, ok.Load(), left)
		}
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
