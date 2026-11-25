package sqlite_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

// SQLite accepts any value in any column, so a wrong declared type or a
// missing constraint shows only in the schema pragmas. A TEXT PRIMARY KEY
// reports notnull 0 in SQLite, so "pk" columns carry no "notnull".
var wantSchema = map[string]struct{ cols, indexes []string }{
	"organizers": {
		[]string{"id TEXT pk", "email TEXT notnull", "password_hash TEXT notnull"},
		[]string{"sqlite_autoindex_organizers_1(id) unique pk", "sqlite_autoindex_organizers_2(email) unique u"},
	},
	"sessions": {
		[]string{"id_hash TEXT pk", "organizer_id TEXT notnull", "created_at BIGINT notnull", "last_seen_at BIGINT notnull"},
		[]string{"sqlite_autoindex_sessions_1(id_hash) unique pk"},
	},
	"events": {
		[]string{"id TEXT pk", "organizer_id TEXT notnull", "title_ja TEXT notnull", "title_en TEXT notnull",
			"starts_at BIGINT notnull", "ends_at BIGINT notnull", "capacity INTEGER notnull", "price BIGINT notnull",
			"sale_starts_at BIGINT notnull", "next_ticket_number INTEGER notnull"},
		[]string{"sqlite_autoindex_events_1(id) unique pk"},
	},
	"purchases": {
		[]string{"id TEXT pk", "event_id TEXT notnull", "email TEXT notnull", "lang TEXT notnull",
			"quantity INTEGER notnull", "amount BIGINT notnull", "state TEXT notnull", "expires_at BIGINT notnull",
			"payment_id TEXT", "checkout_url TEXT", "ticket_number_from INTEGER", "updated_at BIGINT notnull"},
		[]string{"purchases_event_email_state(event_id,email,state) c", "purchases_state_expires_at(state,expires_at) c",
			"sqlite_autoindex_purchases_1(id) unique pk"},
	},
	"event_sales": {
		[]string{"event_id TEXT pk", "held INTEGER notnull", "sold INTEGER notnull", "remaining INTEGER notnull", "refunds INTEGER notnull"},
		[]string{"sqlite_autoindex_event_sales_1(event_id) unique pk"},
	},
	"orphan_refunds": {
		[]string{"payment_id TEXT pk", "purchase_id TEXT notnull", "amount BIGINT notnull", "state TEXT notnull", "updated_at BIGINT notnull"},
		[]string{"sqlite_autoindex_orphan_refunds_1(payment_id) unique pk"},
	},
	"mail_outbox": {
		[]string{"id INTEGER pk", "purchase_id TEXT notnull", "kind TEXT notnull", "lang TEXT notnull", "sent_at BIGINT"},
		nil,
	},
	"billing_logs": {
		[]string{"purchase_id TEXT notnull", "payment_id TEXT notnull", "kind TEXT notnull", "amount BIGINT notnull", "at BIGINT notnull"},
		nil,
	},
	"access_logs": {
		[]string{"method TEXT notnull", "path TEXT notnull", "status INTEGER notnull", "duration_ms BIGINT notnull", "at BIGINT notnull"},
		nil,
	},
}

func strs(t *testing.T, s *store.Store, query string) []string {
	t.Helper()
	var out []string
	if err := s.Read.NewRaw(query).Scan(t.Context(), &out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

func TestSchema(t *testing.T) {
	s := open(t)
	tables := strs(t, s, "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if want := slices.Sorted(maps.Keys(wantSchema)); !slices.Equal(tables, want) {
		t.Fatalf("tables = %v, want %v", tables, want)
	}
	for table, want := range wantSchema {
		cols := strs(t, s, "SELECT name || ' ' || type || iif(\"notnull\", ' notnull', '') || iif(pk, ' pk', '') FROM pragma_table_info('"+table+"') ORDER BY cid")
		if !slices.Equal(cols, want.cols) {
			t.Errorf("%s columns = %q, want %q", table, cols, want.cols)
		}
		idx := strs(t, s, "SELECT l.name || '(' || (SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_index_info(l.name) ORDER BY seqno)) || ')'"+
			" || iif(l.\"unique\", ' unique', '') || ' ' || l.origin FROM pragma_index_list('"+table+"') l ORDER BY l.name")
		if !slices.Equal(idx, want.indexes) {
			t.Errorf("%s indexes = %q, want %q", table, idx, want.indexes)
		}
	}
}

func TestPoolSizes(t *testing.T) {
	s := open(t)
	if got := s.Write.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("write pool = %d, want 1", got)
	}
	if got := s.Read.Stats().MaxOpenConnections; got != 8 {
		t.Errorf("read pool = %d, want 8", got)
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

func TestOpenRejectsURICharacters(t *testing.T) {
	for _, name := range []string{"a?b", "a#b"} {
		dir := t.TempDir()
		if _, err := fsqlite.Open(t.Context(), filepath.Join(dir, name, "f.db")); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("Open with %q = %v, want an error naming the path", name, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("Open with %q created %v", name, entries)
		}
	}
}
