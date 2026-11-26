package servertest_test

import (
	"database/sql"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/okamyuji/firstline/internal/store/servertest"
	_ "github.com/uptrace/bun/driver/pgdriver"
)

func TestServers(t *testing.T) {
	for _, c := range []struct {
		driver string
		start  func(*testing.T) *servertest.Server
	}{{"pg", servertest.Postgres}, {"mysql", servertest.MySQL}} {
		t.Run(c.driver, func(t *testing.T) {
			srv := c.start(t)
			a, b := srv.DB(t), srv.DB(t)
			if a == b {
				t.Fatalf("two databases share the DSN %q", a)
			}
			for _, dsn := range []string{a, b} {
				db, err := sql.Open(c.driver, dsn)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				if err := db.PingContext(t.Context()); err != nil {
					t.Fatalf("ping %s: %v", dsn, err)
				}
			}
		})
	}
}
