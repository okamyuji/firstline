package store_test

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/sqlite"
)

func withSQL(t *testing.T, files fstest.MapFS) *store.Store {
	t.Helper()
	s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	s.SQL = files
	return s
}

func file(q string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(q)} }

func TestCheckDurability(t *testing.T) {
	ok := withSQL(t, fstest.MapFS{"sql/check_durability.sql": file("SELECT 'fsync' AS name, 'off' AS setting WHERE 1 = 0")})
	if err := ok.CheckDurability(t.Context()); err != nil {
		t.Errorf("no rows: %v, want nil", err)
	}
	off := withSQL(t, fstest.MapFS{"sql/check_durability.sql": file("SELECT 'fsync' AS name, 'off' AS setting")})
	if err := off.CheckDurability(t.Context()); err == nil || !strings.Contains(err.Error(), "fsync=off") {
		t.Errorf("fsync off: %v, want an error naming fsync=off", err)
	}
	missing := withSQL(t, fstest.MapFS{})
	if err := missing.CheckDurability(t.Context()); err == nil {
		t.Error("missing file: nil, want an error")
	}
}

func TestQuery(t *testing.T) {
	s := withSQL(t, fstest.MapFS{"sql/q.sql": file("SELECT ? AS a UNION ALL SELECT 2")})
	rows, err := s.Query(t.Context(), s.Read, "q", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []int
	for rows.Next() {
		var a int
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		got = append(got, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Errorf("rows = %v, want [1 2]", got)
	}
	if _, err := s.Query(t.Context(), s.Read, "q"); err == nil {
		t.Error("missing argument: nil, want an arity error")
	}
}

func TestIsConnLost(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("commit: %w", driver.ErrBadConn), true},
		{fmt.Errorf("read: %w", io.EOF), true},
		{io.ErrUnexpectedEOF, true},
		{&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, true},
		{errors.New("duplicate key"), false},
		{nil, false},
	} {
		if got := store.IsConnLost(c.err); got != c.want {
			t.Errorf("IsConnLost(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
