package migrate_test

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/okamyuji/firstline/internal/app"
	"github.com/okamyuji/firstline/internal/inventory"
	"github.com/okamyuji/firstline/internal/migrate"
	"github.com/okamyuji/firstline/internal/payment/fake"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/mysql"
	"github.com/okamyuji/firstline/internal/store/postgres"
	"github.com/okamyuji/firstline/internal/store/servertest"
	"github.com/okamyuji/firstline/internal/store/sqlite"
	"github.com/okamyuji/firstline/internal/store/writer"
	"github.com/uptrace/bun"
)

var now = time.UnixMilli(1_800_000_000_000).UTC()

func closeOnCleanup(t *testing.T, s *store.Store) *store.Store {
	t.Helper()
	t.Cleanup(func() { _ = s.Write.Close(); _ = s.Read.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func openSQLite(t *testing.T) *store.Store {
	t.Helper()
	s, err := sqlite.Open(t.Context(), filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	return closeOnCleanup(t, s)
}

func exec(t *testing.T, s *store.Store, name string, args ...any) {
	t.Helper()
	if _, err := s.Exec(t.Context(), s.Write, name, args...); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// seed puts rows in every table; withMail false leaves mail_outbox empty.
func seed(t *testing.T, s *store.Store, withMail bool) {
	t.Helper()
	ms := now.UnixMilli()
	exec(t, s, "insert_organizer", "org1", "o@example.com", "hash")
	exec(t, s, "insert_session", "h1", "org1", ms, ms)
	exec(t, s, "insert_event", "e1", "org1", "題名", "Title", ms+86_400_000, ms+90_000_000, 10, 5000, ms-3_600_000, 1)
	exec(t, s, "insert_event_sales", "e1", 10)
	exec(t, s, "insert_purchase", "p1", "e1", "a@example.com", "ja", 2, int64(10000), ms+600_000, ms)
	exec(t, s, "insert_purchase", "p2", "e1", "b@example.com", "en", 1, int64(5000), ms+600_000, ms)
	exec(t, s, "record_payment", "pay1", "https://pay.example/1", "p1")
	exec(t, s, "insert_orphan", "pay9", "p9", 7000, ms)
	exec(t, s, "insert_billing", "p1", "pay1", "charge", 10000, ms)
	exec(t, s, "insert_billing", "p1", "pay1", "refund", 10000, ms+1)
	exec(t, s, "insert_access_log", "GET", "/", 200, int64(3), ms)
	if withMail {
		exec(t, s, "insert_mail", "p1", "held", "ja")
		exec(t, s, "insert_mail", "p2", "held", "en")
		exec(t, s, "insert_mail", "p1", "purchased", "ja")
		exec(t, s, "mark_mail_sent", ms, 1)
	}
}

type totals struct{ rows, amount int64 }

// tally counts each table's rows and sums its amount column with bun's builder, apart from the code under test.
func tally(t *testing.T, s *store.Store) map[string]totals {
	t.Helper()
	out := map[string]totals{}
	for _, table := range migrate.Tables {
		n, err := s.Write.NewSelect().TableExpr(table).Count(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var sum int64
		if table == "purchases" || table == "billing_logs" || table == "orphan_refunds" {
			if err := s.Write.NewSelect().TableExpr(table).ColumnExpr("COALESCE(SUM(amount), 0)").Scan(t.Context(), &sum); err != nil {
				t.Fatal(err)
			}
		}
		out[table] = totals{int64(n), sum}
	}
	return out
}

type nopReporter struct{}

func (nopReporter) Report(context.Context, error, ...slog.Attr) {}

// hold creates a hold through the app and the real writer, which inserts a mail_outbox row with a database-assigned id.
func hold(t *testing.T, s *store.Store) {
	t.Helper()
	w, err := writer.New(s.Write, writer.Options{
		QueueCap: 16, BatchMax: 16, BatchWait: time.Millisecond,
		IsIOError:    s.IsIOError,
		OnFatal:      func(err error) { t.Errorf("writer fatal: %v", err) },
		Now:          time.Now,
		After:        time.After,
		OnSyncResult: func(time.Time, time.Duration) {},
		OnAsyncError: func(err error) { t.Errorf("async error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run()
	t.Cleanup(w.Stop)
	pay := fake.New(time.Now, func(time.Duration) <-chan time.Time { return nil }, time.Second, fake.Complete)
	t.Cleanup(pay.Stop)
	svc := app.New(app.Deps{
		Store: s, Writer: w, Inventory: inventory.NewDB(), Payment: pay, Errors: nopReporter{},
		Clock: func() time.Time { return now },
	})
	if _, err := svc.Hold(t.Context(), app.HoldInput{EventID: "e1", Email: "c@example.com", Lang: "en", Quantity: 1}); err != nil {
		t.Fatalf("hold on the target: %v", err)
	}
}

// queryLog records every query a store runs.
type queryLog struct {
	mu sync.Mutex
	qs []string
}

func (l *queryLog) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context { return ctx }

func (l *queryLog) AfterQuery(_ context.Context, e *bun.QueryEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.qs = append(l.qs, e.Query)
}

func (l *queryLog) ran(part string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, q := range l.qs {
		if strings.Contains(q, part) {
			return true
		}
	}
	return false
}

func logged(s *store.Store) (*store.Store, *queryLog) {
	l := &queryLog{}
	c := *s
	c.Write = s.Write.WithQueryHook(l)
	return &c, l
}

func TestRunOnServers(t *testing.T) {
	for _, d := range []struct {
		name   string
		server func(*testing.T) *servertest.Server
		open   func(context.Context, string) (*store.Store, error)
		setval bool
	}{
		{"postgres", servertest.Postgres, postgres.Open, true},
		{"mysql", servertest.MySQL, mysql.Open, false},
	} {
		t.Run(d.name, func(t *testing.T) {
			srv := d.server(t)
			target := func(t *testing.T) *store.Store {
				s, err := d.open(t.Context(), srv.DB(t))
				if err != nil {
					t.Fatal(err)
				}
				return closeOnCleanup(t, s)
			}

			t.Run("copies every table and continues mail ids", func(t *testing.T) {
				src, dst := openSQLite(t), target(t)
				seed(t, src, true)
				dst, log := logged(dst)
				if err := migrate.Run(t.Context(), src, dst); err != nil {
					t.Fatal(err)
				}
				want, got := tally(t, src), tally(t, dst)
				for _, table := range migrate.Tables {
					if want[table].rows == 0 || got[table] != want[table] {
						t.Errorf("%s: target %+v, source %+v", table, got[table], want[table])
					}
				}
				if log.ran("setval") != d.setval {
					t.Errorf("setval ran = %v, want %v", !d.setval, d.setval)
				}
				hold(t, dst)
				if n := tally(t, dst)["mail_outbox"].rows; n != want["mail_outbox"].rows+1 {
					t.Errorf("mail_outbox after the hold = %d rows, want %d", n, want["mail_outbox"].rows+1)
				}
			})

			t.Run("empty mail_outbox skips setval", func(t *testing.T) {
				src, dst := openSQLite(t), target(t)
				seed(t, src, false)
				dst, log := logged(dst)
				if err := migrate.Run(t.Context(), src, dst); err != nil {
					t.Fatal(err)
				}
				if log.ran("setval") {
					t.Error("setval ran with no mail_outbox rows")
				}
				hold(t, dst)
			})

			if d.setval {
				t.Run("setval error rolls back", func(t *testing.T) {
					src, dst := openSQLite(t), target(t)
					seed(t, src, true)
					err := migrate.Run(t.Context(), src, withFiles(t, dst, map[string]string{"setval_mail_outbox": "SELECT nope"}))
					if err == nil || !strings.Contains(err.Error(), "setval mail_outbox") {
						t.Errorf("Run = %v, want a setval error", err)
					}
					if n := tally(t, dst)["mail_outbox"].rows; n != 0 {
						t.Errorf("a failed run left %d mail rows in the target", n)
					}
				})
			}

			t.Run("refuses a target with rows", func(t *testing.T) {
				src, dst := openSQLite(t), target(t)
				seed(t, src, true)
				exec(t, dst, "insert_access_log", "GET", "/x", 200, int64(1), int64(1))
				err := migrate.Run(t.Context(), src, dst)
				if err == nil || !strings.Contains(err.Error(), "target table access_logs has 1 rows") {
					t.Errorf("Run = %v, want the refusal naming access_logs", err)
				}
				if n := tally(t, dst)["organizers"].rows; n != 0 {
					t.Errorf("organizers copied into a refused target: %d", n)
				}
			})
		})
	}
}

// withFiles returns s with some of its SQL files replaced.
func withFiles(t *testing.T, s *store.Store, files map[string]string) *store.Store {
	t.Helper()
	m := fstest.MapFS{}
	entries, err := fs.ReadDir(s.SQL, "sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(s.SQL, "sql/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		m["sql/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	for name, q := range files {
		m["sql/"+name+".sql"] = &fstest.MapFile{Data: []byte(q)}
	}
	c := *s
	c.SQL = m
	return &c
}

func TestRunSQLiteToSQLite(t *testing.T) {
	src := openSQLite(t)
	seed(t, src, true)

	t.Run("copies every table", func(t *testing.T) {
		dst := openSQLite(t)
		if err := migrate.Run(t.Context(), src, dst); err != nil {
			t.Fatal(err)
		}
		want, got := tally(t, src), tally(t, dst)
		for _, table := range migrate.Tables {
			if got[table] != want[table] {
				t.Errorf("%s: target %+v, source %+v", table, got[table], want[table])
			}
		}
	})

	for _, c := range []struct {
		name, file, query, want string
	}{
		{"row count", "import_access_logs",
			"INSERT INTO access_logs (method, path, status, duration_ms, at) SELECT ?, ?, ?, ?, ? WHERE 1 = 0",
			"access_logs: 0 rows in the target, 1 in the source"},
		{"amount sum", "import_billing_logs",
			"INSERT INTO billing_logs (purchase_id, payment_id, kind, amount, at) VALUES (?, ?, ?, ? + 1, ?)",
			"billing_logs: amount sum 20002 in the target, 20000 in the source"},
		{"purchases amount", "import_purchases",
			"INSERT INTO purchases (id, event_id, email, lang, quantity, amount, state, expires_at, payment_id, checkout_url, ticket_number_from, updated_at) VALUES (?, ?, ?, ?, ?, ? - 1, ?, ?, ?, ?, ?, ?)",
			"purchases: amount sum 14998 in the target, 15000 in the source"},
		{"orphan amount", "import_orphan_refunds",
			"INSERT INTO orphan_refunds (payment_id, purchase_id, amount, state, updated_at) VALUES (?, ?, ? * 2, ?, ?)",
			"orphan_refunds: amount sum 14000 in the target, 7000 in the source"},
	} {
		t.Run("detects a "+c.name+" mismatch", func(t *testing.T) {
			dst := withFiles(t, openSQLite(t), map[string]string{c.file: c.query})
			err := migrate.Run(t.Context(), src, dst)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Run = %v, want %q", err, c.want)
			}
			if n := tally(t, dst)["organizers"].rows; n != 0 {
				t.Errorf("a failed run left %d organizers in the target", n)
			}
		})
	}

	t.Run("refuses a target with rows", func(t *testing.T) {
		dst := openSQLite(t)
		exec(t, dst, "insert_access_log", "GET", "/x", 200, int64(1), int64(1))
		err := migrate.Run(t.Context(), src, dst)
		if err == nil || !strings.Contains(err.Error(), "target table access_logs has 1 rows") {
			t.Errorf("Run = %v, want the refusal naming access_logs", err)
		}
		if n := tally(t, dst)["organizers"].rows; n != 0 {
			t.Errorf("organizers copied into a refused target: %d", n)
		}
	})

	t.Run("target write error", func(t *testing.T) {
		dst := withFiles(t, openSQLite(t), map[string]string{"import_events": "INSERT INTO nope VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"})
		if err := migrate.Run(t.Context(), src, dst); err == nil || !strings.Contains(err.Error(), "write events") {
			t.Errorf("Run = %v, want a write events error", err)
		}
	})

	for amount, want := range map[string]string{"'x'": `parsing "x"`, "1.5": "amount is float64"} {
		t.Run("non-integer amount "+amount, func(t *testing.T) {
			bad := withFiles(t, src, map[string]string{"export_orphan_refunds": "SELECT payment_id, purchase_id, " + amount + " AS amount, state, updated_at FROM orphan_refunds"})
			if err := migrate.Run(t.Context(), bad, openSQLite(t)); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Run = %v, want %q", err, want)
			}
		})
	}

	t.Run("source read error", func(t *testing.T) {
		bad := withFiles(t, src, map[string]string{"export_events": "SELECT nope FROM events"})
		if err := migrate.Run(t.Context(), bad, openSQLite(t)); err == nil || !strings.Contains(err.Error(), "events") {
			t.Errorf("Run = %v, want an error naming events", err)
		}
	})
}
