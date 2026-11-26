package mysql_test

import (
	"context"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/mysql"
	"github.com/okamyuji/firstline/internal/store/servertest"
	"github.com/okamyuji/firstline/internal/store/storetest"
)

func open(t *testing.T, dsn string) *store.Store {
	t.Helper()
	s, err := mysql.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	return s
}

func migrate(t *testing.T, s *store.Store) *store.Store {
	t.Helper()
	// The second run proves the schema is idempotent, as every start runs it.
	for range 2 {
		if err := s.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func rowsAffected(t *testing.T, s *store.Store, name string, args ...any) int64 {
	t.Helper()
	res, err := s.Exec(t.Context(), s.Write, name, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMySQL(t *testing.T) {
	srv := servertest.MySQL(t)
	migrated := func(t *testing.T) *store.Store { return migrate(t, open(t, srv.DB(t))) }

	t.Run("storetest", func(t *testing.T) { storetest.Run(t, migrated) })

	t.Run("innodb_flush_log_at_trx_commit 2", func(t *testing.T) {
		ctx := t.Context()
		if _, err := srv.Admin.ExecContext(ctx, "SET GLOBAL innodb_flush_log_at_trx_commit = 2"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := srv.Admin.ExecContext(context.Background(), "SET GLOBAL innodb_flush_log_at_trx_commit = 1"); err != nil {
				t.Error(err)
			}
		})
		_, err := mysql.Open(ctx, srv.DB(t))
		if err == nil || !strings.Contains(err.Error(), "innodb_flush_log_at_trx_commit=2") {
			t.Errorf("Open = %v, want an error naming innodb_flush_log_at_trx_commit=2", err)
		}
	})

	t.Run("clientFoundRows in the DSN is turned off", func(t *testing.T) {
		cfg, err := mysqldriver.ParseDSN(srv.DB(t))
		if err != nil {
			t.Fatal(err)
		}
		cfg.ClientFoundRows = true
		s := migrate(t, open(t, cfg.FormatDSN()))
		rowsAffected(t, s, "insert_orphan", "pay1", "p1", 100, int64(1))
		if n := rowsAffected(t, s, "insert_orphan", "pay1", "p2", 200, int64(2)); n != 0 {
			t.Errorf("duplicate insert_orphan affected %d rows, want 0", n)
		}
	})

	t.Run("insert_orphan keeps other errors", func(t *testing.T) {
		s := migrated(t)
		if _, err := s.Exec(t.Context(), s.Write, "insert_orphan", "pay1", nil, 100, int64(1)); err == nil {
			t.Error("NULL purchase_id: nil, want an error")
		}
	})

	t.Run("ids and emails compare case-sensitively", func(t *testing.T) {
		s := migrated(t)
		rowsAffected(t, s, "insert_organizer", "Ab", "a@example.com", "h")
		rowsAffected(t, s, "insert_organizer", "ab", "A@example.com", "h")
		var o struct {
			ID           string `bun:"id"`
			Email        string `bun:"email"`
			PasswordHash string `bun:"password_hash"`
		}
		if err := s.Select(t.Context(), s.Write, &o, "get_organizer_by_email", "A@example.com"); err != nil || o.ID != "ab" {
			t.Errorf("organizer by A@example.com = %q, %v, want ab", o.ID, err)
		}
	})

	t.Run("error kinds", func(t *testing.T) {
		s := migrated(t)
		ctx := t.Context()
		rowsAffected(t, s, "insert_organizer", "o1", "a@example.com", "h")
		_, dup := s.Exec(ctx, s.Write, "insert_organizer", "o1", "b@example.com", "h")
		if !s.IsUnique(dup) || s.IsIOError(dup) {
			t.Errorf("duplicate id: unique %v, I/O %v, want true and false (%v)", s.IsUnique(dup), s.IsIOError(dup), dup)
		}

		tx, err := s.Write.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := tx.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.Admin.ExecContext(ctx, "KILL ?", id); err != nil {
			t.Fatal(err)
		}
		_, lost := s.Exec(ctx, tx, "insert_organizer", "o2", "c@example.com", "h")
		if !s.IsIOError(lost) || s.IsUnique(lost) {
			t.Errorf("lost connection: I/O %v, unique %v, want true and false (%v)", s.IsIOError(lost), s.IsUnique(lost), lost)
		}
	})
}

func TestOpenRejectsBadDSN(t *testing.T) {
	if _, err := mysql.Open(t.Context(), "not a dsn"); err == nil {
		t.Error("Open with a malformed DSN = nil, want an error")
	}
}
