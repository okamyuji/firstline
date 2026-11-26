package postgres_test

import (
	"strings"
	"testing"

	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/postgres"
	"github.com/okamyuji/firstline/internal/store/servertest"
	"github.com/okamyuji/firstline/internal/store/storetest"
)

func open(t *testing.T, dsn string) *store.Store {
	t.Helper()
	s, err := postgres.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	return s
}

func TestPostgres(t *testing.T) {
	srv := servertest.Postgres(t)
	migrated := func(t *testing.T) *store.Store {
		s := open(t, srv.DB(t))
		// The second run proves the schema is idempotent, as every start runs it.
		for range 2 {
			if err := s.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}

	t.Run("storetest", func(t *testing.T) { storetest.Run(t, migrated) })

	t.Run("synchronous_commit off", func(t *testing.T) {
		// pgdriver sends unknown DSN parameters as session settings, as an operator's DSN could.
		_, err := postgres.Open(t.Context(), srv.DB(t)+"&synchronous_commit=off")
		if err == nil || !strings.Contains(err.Error(), "synchronous_commit=off") {
			t.Errorf("Open = %v, want an error naming synchronous_commit=off", err)
		}
	})

	t.Run("error kinds", func(t *testing.T) {
		s := migrated(t)
		ctx := t.Context()
		if _, err := s.Exec(ctx, s.Write, "insert_organizer", "o1", "a@example.com", "h"); err != nil {
			t.Fatal(err)
		}
		_, dup := s.Exec(ctx, s.Write, "insert_organizer", "o1", "b@example.com", "h")
		if !s.IsUnique(dup) || s.IsIOError(dup) {
			t.Errorf("duplicate id: unique %v, I/O %v, want true and false (%v)", s.IsUnique(dup), s.IsIOError(dup), dup)
		}

		tx, err := s.Write.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var pid int
		if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.Admin.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
			t.Fatal(err)
		}
		_, lost := s.Exec(ctx, tx, "insert_organizer", "o2", "c@example.com", "h")
		if !s.IsIOError(lost) || s.IsUnique(lost) {
			t.Errorf("lost connection: I/O %v, unique %v, want true and false (%v)", s.IsIOError(lost), s.IsUnique(lost), lost)
		}
	})
}

func TestOpenRejectsBadDSN(t *testing.T) {
	if _, err := postgres.Open(t.Context(), "mysql://localhost/x"); err == nil {
		t.Error("Open with a mysql:// DSN = nil, want an error")
	}
}
