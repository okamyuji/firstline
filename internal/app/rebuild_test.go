package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/port"
	"github.com/okamyuji/firstline/internal/store"
	fsqlite "github.com/okamyuji/firstline/internal/store/sqlite"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := fsqlite.Open(t.Context(), t.TempDir()+"/f.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func seed(t *testing.T, s *store.Store, id string, capacity int) {
	t.Helper()
	if _, err := s.Exec(t.Context(), s.Write, "insert_event", id, "o", "j", "e", 2, 3, capacity, 5000, 1, 1); err != nil {
		t.Fatal(err)
	}
}

func buy(t *testing.T, s *store.Store, id, ev, state string, qty int) {
	t.Helper()
	if _, err := s.Exec(t.Context(), s.Write, "insert_purchase", id, ev, id+"@x", "ja", qty, qty*5000, 9, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write.ExecContext(t.Context(), "UPDATE purchases SET state = ? WHERE id = ?", state, id); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildInventory(t *testing.T) {
	s := openStore(t)
	seed(t, s, "e1", 10)
	seed(t, s, "e2", 3)
	buy(t, s, "p1", "e1", "held", 2)
	buy(t, s, "p2", "e1", "purchased", 3)
	buy(t, s, "p3", "e1", "expired", 4)

	inv := inventory.NewMemory()
	svc := app.New(app.Deps{Store: s, Inventory: inv})
	if err := svc.RebuildInventory(t.Context()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"e1": 5, "e2": 3} {
		got, err := inv.Remaining(t.Context(), id)
		if err != nil || got != want {
			t.Errorf("%s remaining = %d, %v; want %d", id, got, err, want)
		}
	}
}

func TestRebuildInventoryReadError(t *testing.T) {
	s := openStore(t)
	_ = s.Read.Close()
	svc := app.New(app.Deps{Store: s, Inventory: inventory.NewMemory()})
	if err := svc.RebuildInventory(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

type addFails struct{ port.Inventory }

func (addFails) Add(context.Context, string, int) error { return errors.New("boom") }

func TestRebuildInventoryAddError(t *testing.T) {
	s := openStore(t)
	seed(t, s, "e1", 1)
	svc := app.New(app.Deps{Store: s, Inventory: addFails{}})
	if err := svc.RebuildInventory(t.Context()); err == nil {
		t.Fatal("want error")
	}
}
