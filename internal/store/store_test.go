package store

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestSplitStatements(t *testing.T) {
	got := splitStatements("A\n  x;\n\nB y;  \nC")
	want := []string{"A\n  x;\n", "\nB y;  \n", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if n := len(splitStatements("")); n != 0 {
		t.Fatalf("empty gave %d", n)
	}
}

func TestMissingSQL(t *testing.T) {
	s := &Store{SQL: fstest.MapFS{}}
	if _, err := s.Exec(t.Context(), nil, "x"); err == nil {
		t.Error("Exec: want error")
	}
	if err := s.Select(t.Context(), nil, nil, "x"); err == nil {
		t.Error("Select: want error")
	}
	if err := s.Migrate(t.Context()); err == nil {
		t.Error("Migrate: want error")
	}
}
