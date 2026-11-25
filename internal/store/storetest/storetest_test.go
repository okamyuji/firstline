package storetest_test

import (
	"path/filepath"
	"testing"

	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/sqlite"
	"github.com/okamyuji/firstline/internal/store/storetest"
)

// The suite lives in this package's test so its own statements count toward coverage and mutation testing.
func TestSQLite(t *testing.T) {
	storetest.Run(t, func(t *testing.T) *store.Store {
		s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "f.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
		if err := s.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		return s
	})
}
