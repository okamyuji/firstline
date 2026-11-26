package migrate_test

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/okamyuji/firstline/internal/migrate"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/okamyuji/firstline/internal/store/mysql"
	"github.com/okamyuji/firstline/internal/store/postgres"
	"github.com/okamyuji/firstline/internal/store/servertest"
	"github.com/uptrace/bun/dialect"
)

// big is above 2^53, where a float64 would round it.
const big = int64(1)<<53 + 1

// seedEdges fills every table with values that a type or column mix-up would
// change: non-ASCII text, quotes and backslashes, NULLs, long strings, a
// sent mail and amounts above 2^53. mail_outbox gets exactly one row.
func seedEdges(t *testing.T, s *store.Store) {
	t.Helper()
	ms := now.UnixMilli()
	odd := `'"?%\` + strings.Repeat("長", 2000)
	exec(t, s, "insert_organizer", "org1", "o@例え.jp", "hash"+odd)
	exec(t, s, "insert_session", "h1", "org1", ms-5, ms)
	exec(t, s, "insert_event", "e1", "org1", "題名🎫"+odd, "Title "+odd, ms+86_400_000, ms+90_000_000, 10, 5000, ms-3_600_000, 1)
	exec(t, s, "insert_event_sales", "e1", 10)
	exec(t, s, "insert_purchase", "p1", "e1", "a@例え.jp", "ja", 2, big, ms+600_000, ms)
	exec(t, s, "insert_purchase", "p2", "e1", "b@example.com", "en", 1, int64(5000), ms+700_000, ms)
	exec(t, s, "record_payment", "pay1", "https://pay.example/1?q="+odd, "p1")
	exec(t, s, "insert_orphan", "pay9", "p9", big, ms)
	exec(t, s, "insert_billing", "p1", "pay1", "charge", big, ms)
	exec(t, s, "insert_access_log", "GET", "/Events/"+odd, 200, int64(3), ms)
	exec(t, s, "insert_mail", "p1", "held", "ja")
	exec(t, s, "mark_mail_sent", ms, 1)
}

// dump renders every row of every table as sorted "column=type:value" text,
// read with plain SQL apart from the code under test. A []byte on SQLite or
// PostgreSQL is a BLOB or bytea, which no column should hold. MySQL's text
// protocol returns every value as bytes, so its column type decides.
func dump(t *testing.T, s *store.Store) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, table := range migrate.Tables {
		out[table] = dumpTable(t, s, table)
	}
	return out
}

func dumpTable(t *testing.T, s *store.Store, table string) []string {
	t.Helper()
	isMySQL := s.Read.Dialect().Name() == dialect.MySQL
	rows, err := s.Read.QueryContext(t.Context(), "SELECT * FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	types, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		vals := make([]any, len(types))
		ptrs := make([]any, len(types))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		cells := make([]string, len(types))
		for i, ct := range types {
			cells[i] = ct.Name() + "=" + cell(t, vals[i], ct, isMySQL)
		}
		slices.Sort(cells)
		out = append(out, strings.Join(cells, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func cell(t *testing.T, v any, ct *sql.ColumnType, isMySQL bool) string {
	t.Helper()
	switch v := v.(type) {
	case nil:
		return "NULL"
	case int64:
		return "I:" + strconv.FormatInt(v, 10)
	case string:
		return "S:" + v
	case []byte:
		if !isMySQL {
			return fmt.Sprintf("BLOB:%x", v)
		}
		if strings.Contains(ct.DatabaseTypeName(), "INT") {
			n, err := strconv.ParseInt(string(v), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return "I:" + strconv.FormatInt(n, 10)
		}
		return "S:" + string(v)
	}
	t.Fatalf("column %s holds %T", ct.Name(), v)
	return ""
}

// lastMailID is the largest mail_outbox id.
func lastMailID(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var id int64
	if err := s.Read.QueryRowContext(t.Context(), "SELECT MAX(id) FROM mail_outbox").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRunKeepsEveryColumnBetweenDialects(t *testing.T) {
	pg, my := servertest.Postgres(t), servertest.MySQL(t)
	opens := map[string]func(*testing.T) *store.Store{
		"sqlite": openSQLite,
		"postgres": func(t *testing.T) *store.Store {
			s, err := postgres.Open(t.Context(), pg.DB(t))
			if err != nil {
				t.Fatal(err)
			}
			return closeOnCleanup(t, s)
		},
		"mysql": func(t *testing.T) *store.Store {
			s, err := mysql.Open(t.Context(), my.DB(t))
			if err != nil {
				t.Fatal(err)
			}
			return closeOnCleanup(t, s)
		},
	}
	for _, from := range []string{"sqlite", "postgres", "mysql"} {
		for _, to := range []string{"sqlite", "postgres", "mysql"} {
			if from == to {
				continue
			}
			t.Run(from+"->"+to, func(t *testing.T) {
				src, dst := opens[from](t), opens[to](t)
				seedEdges(t, src)
				if err := migrate.Run(t.Context(), src, dst); err != nil {
					t.Fatal(err)
				}
				want, got := dump(t, src), dump(t, dst)
				for _, table := range migrate.Tables {
					if len(want[table]) == 0 || !slices.Equal(got[table], want[table]) {
						t.Errorf("%s differs:\nsource %q\ntarget %q", table, want[table], got[table])
					}
				}
				// The next id must follow the copied ones, or the first hold after
				// the migration collides with a copied mail.
				before := lastMailID(t, dst)
				hold(t, dst)
				if after := lastMailID(t, dst); after != before+1 {
					t.Errorf("next mail id = %d; want %d", after, before+1)
				}
			})
		}
	}
}

func TestTablesListsEveryTableOfEverySchema(t *testing.T) {
	create := regexp.MustCompile(`(?m)^CREATE TABLE IF NOT EXISTS (\w+)`)
	want := slices.Sorted(slices.Values(migrate.Tables))
	for _, d := range []string{"sqlite", "postgres", "mysql"} {
		b, err := os.ReadFile("../store/" + d + "/sql/schema.sql")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, m := range create.FindAllStringSubmatch(string(b), -1) {
			got = append(got, m[1])
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s schema tables %v; migrate.Tables %v", d, got, want)
		}
	}
}
