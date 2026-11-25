package store

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestSplitStatements(t *testing.T) {
	got := splitStatements("A\n  x;\n\nB y;  \n-- a note;\nC")
	want := []string{"A\n  x;\n", "\nB y;  \n", "C"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if n := len(splitStatements("")); n != 0 {
		t.Fatalf("empty gave %d", n)
	}
	if got := splitStatements("-- only a note;\n"); len(got) != 0 {
		t.Fatalf("comment-only schema gave %q", got)
	}
}

func TestPlaceholders(t *testing.T) {
	for _, c := range []struct {
		q    string
		want int
	}{
		{"", 0},
		{"SELECT ?", 1},
		{"UPDATE t SET a = ?, b = ? WHERE c = ? AND d >= ?", 4},
		{"SELECT '?', \"?\" FROM t WHERE a = ?", 1},
		{"SELECT 'it''s ?' WHERE a = ?", 1},
		{"-- args: ?\nSELECT ?", 1},
		{"SELECT ? -- trailing ?\n, ?", 2},
		{"SELECT /* ? */ ?", 1},
		{"SELECT a - -1, ?", 1},
		{"SELECT a / 2, ?", 1},
		{"SELECT 'open ?", 0},
		{"SELECT ? /* open ?", 1},
	} {
		if got := Placeholders(c.q); got != c.want {
			t.Errorf("Placeholders(%q) = %d, want %d", c.q, got, c.want)
		}
	}
}

func TestArityMismatch(t *testing.T) {
	s := &Store{SQL: fstest.MapFS{"sql/q.sql": {Data: []byte("-- args: a, b\nSELECT ? WHERE x = ?")}}}
	for _, args := range [][]any{nil, {1}, {1, 2, 3}} {
		if _, err := s.Exec(t.Context(), nil, "q", args...); err == nil {
			t.Errorf("Exec with %d args: want error", len(args))
		}
		if err := s.Select(t.Context(), nil, nil, "q", args...); err == nil {
			t.Errorf("Select with %d args: want error", len(args))
		}
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
