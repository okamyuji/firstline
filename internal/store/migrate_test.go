package store_test

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/okamyuji/firstline/internal/store/sqlite"
)

// Gremlins runs only this package's tests against store.go, so the statement-failure path needs a real database here.
func TestMigrateStatementResult(t *testing.T) {
	s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate with the real schema: %v", err)
	}
	bad := *s
	bad.SQL = fstest.MapFS{"sql/schema.sql": {Data: []byte("NOT SQL;\n")}}
	if err := bad.Migrate(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "migrate:") {
		t.Errorf("Migrate with a broken statement = %v, want a migrate: error", err)
	}
}
