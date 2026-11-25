package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/okamyuji/firstline/internal/store"
	fsqlite "github.com/okamyuji/firstline/internal/store/sqlite"
	msqlite "modernc.org/sqlite"
)

func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := fsqlite.Open(t.Context(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func withInsert(s *store.Store) *store.Store {
	c := *s
	c.SQL = fstest.MapFS{"sql/insert-organizer.sql": {Data: []byte("INSERT INTO organizers(id, email, password_hash) VALUES (?, ?, ?)")}}
	return &c
}

func pragma(t *testing.T, s *store.Store, read bool, name string) string {
	t.Helper()
	db := s.Write
	if read {
		db = s.Read
	}
	var v string
	if err := db.QueryRowContext(t.Context(), "PRAGMA "+name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDSNMatchesDesign(t *testing.T) {
	const p = "/var/lib/firstline/firstline.db"
	w := "file:" + p + "?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=1&_txlock=immediate&_pragma=wal_autocheckpoint(0)&_pragma=cache_size(-16384)"
	r := "file:" + p + "?mode=ro&_busy_timeout=5000&_pragma=mmap_size(268435456)&_pragma=cache_size(-65536)"
	if got := fsqlite.WriteDSN(p); got != w {
		t.Errorf("WriteDSN = %s", got)
	}
	if got := fsqlite.ReadDSN(p); got != r {
		t.Errorf("ReadDSN = %s", got)
	}
}

func TestReadDBRejectsWrites(t *testing.T) {
	s := open(t)
	_, err := s.Read.ExecContext(t.Context(), "INSERT INTO organizers(id,email,password_hash) VALUES('a','a@x','h')")
	if err == nil || !strings.Contains(err.Error(), "readonly database") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadDBDoesNotCreateFile(t *testing.T) {
	path2 := filepath.Join(t.TempDir(), "none2.db")
	db, err := fsqlite.OpenRaw(path2)
	if err == nil {
		_ = db.Close()
		t.Fatal("read-only open of a missing file succeeded")
	}
	if _, statErr := os.Stat(path2); statErr == nil {
		t.Fatal("file was created")
	}
}

func TestPragmasApplied(t *testing.T) {
	s := open(t)
	for _, c := range []struct {
		read       bool
		name, want string
	}{
		{false, "journal_mode", "wal"}, {false, "synchronous", "2"},
		{false, "wal_autocheckpoint", "0"}, {false, "cache_size", "-16384"},
		{true, "mmap_size", "268435456"}, {true, "cache_size", "-65536"},
	} {
		if got := pragma(t, s, c.read, c.name); got != c.want {
			t.Errorf("read=%v %s = %s, want %s", c.read, c.name, got, c.want)
		}
	}
}

func TestOpenOrder(t *testing.T) {
	if _, err := fsqlite.Open(t.Context(), filepath.Join(t.TempDir(), "new.db")); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailsOnBadPath(t *testing.T) {
	if _, err := fsqlite.Open(t.Context(), filepath.Join(t.TempDir(), "no", "dir", "f.db")); err == nil {
		t.Fatal("want error")
	}
}

func TestStrictPragmas(t *testing.T) {
	if _, err := fsqlite.Open(t.Context(), filepath.Join(t.TempDir(), "s.db")); err != nil {
		t.Fatal(err)
	}
	if !msqlite.StrictPragmasEnabled() {
		t.Fatal("strict pragmas not enabled")
	}
	bad := "file:" + filepath.Join(t.TempDir(), "b.db") + "?_pragma=cache_size(1);PRAGMA%20user_version=9"
	if err := fsqlite.PingRaw(t.Context(), bad); err == nil {
		t.Fatal("multi-statement pragma accepted")
	}
}

func TestCommittedRowVisibleToRead(t *testing.T) {
	s := withInsert(open(t))
	ctx := t.Context()
	if _, err := s.Exec(ctx, s.Write, "insert-organizer", "o1", "a@x", "h"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Read.QueryRowContext(ctx, "SELECT count(*) FROM organizers").Scan(&n); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestSchemaTables(t *testing.T) {
	s := open(t)
	var n int
	if err := s.Read.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Fatalf("tables = %d", n)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := open(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestErrorClassifiers(t *testing.T) {
	s := withInsert(open(t))
	ctx := context.Background()
	if _, err := s.Exec(ctx, s.Write, "insert-organizer", "o1", "a@x", "h"); err != nil {
		t.Fatal(err)
	}
	_, dupEmail := s.Exec(ctx, s.Write, "insert-organizer", "o2", "a@x", "h")
	_, dupPK := s.Exec(ctx, s.Write, "insert-organizer", "o1", "b@x", "h")
	for _, err := range []error{dupEmail, dupPK} {
		if !s.IsUnique(err) || s.IsIOError(err) {
			t.Errorf("unique misclassified: %v", err)
		}
	}
	_, ro := s.Read.ExecContext(ctx, "INSERT INTO organizers(id,email,password_hash) VALUES('z','z','z')")
	if s.IsUnique(ro) || s.IsIOError(ro) || s.IsUnique(nil) || s.IsIOError(nil) {
		t.Error("readonly or nil misclassified")
	}
	if s.IsIOError(os.ErrNotExist) || s.IsUnique(os.ErrNotExist) {
		t.Error("foreign error misclassified")
	}
}

func TestSelectAndMigrateError(t *testing.T) {
	s := withInsert(open(t))
	ctx := t.Context()
	if _, err := s.Exec(ctx, s.Write, "insert-organizer", "o1", "a@x", "h"); err != nil {
		t.Fatal(err)
	}
	s.SQL = fstest.MapFS{
		"sql/q.sql":      {Data: []byte("SELECT email FROM organizers WHERE id = ?")},
		"sql/schema.sql": {Data: []byte("NOT SQL;\n")},
	}
	var email string
	if err := s.Select(ctx, s.Read, &email, "q", "o1"); err != nil || email != "a@x" {
		t.Fatalf("email=%q err=%v", email, err)
	}
	if err := s.Migrate(ctx); err == nil {
		t.Fatal("bad schema accepted")
	}
}
