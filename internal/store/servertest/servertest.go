// Package servertest starts PostgreSQL and MySQL containers for the store tests.
package servertest

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"sync/atomic"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	_ "github.com/uptrace/bun/driver/pgdriver"
)

// Server hands out empty databases on one container that lives until the test ends.
type Server struct {
	// Admin is connected as the superuser, for settings a test changes on purpose.
	Admin *sql.DB
	dsn   func(name string) string
	n     atomic.Int64
}

// DB creates an empty database and returns its DSN.
func (s *Server) DB(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("t%d", s.n.Add(1))
	if _, err := s.Admin.ExecContext(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	return s.dsn(name)
}

// start skips the test when Docker is unreachable.
func start(t *testing.T) {
	t.Helper()
	// Ryuk mounts the Docker socket path, which colima's VM cannot create; CleanupContainer removes the containers instead.
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	testcontainers.SkipIfProviderIsNotHealthy(t)
}

// Postgres starts postgres:16-alpine, or skips the test when Docker is unreachable.
func Postgres(t *testing.T) *Server {
	t.Helper()
	start(t)
	ctr, err := tcpostgres.Run(t.Context(), "postgres:16-alpine",
		// The module's default command adds fsync=off, which postgres.Open refuses.
		testcontainers.WithCmd("postgres"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2)))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	base, err := ctr.ConnectionString(t.Context(), "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	dsn := func(name string) string {
		v := *u
		v.Path = "/" + name
		return v.String()
	}
	admin, err := sql.Open("pg", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return &Server{Admin: admin, dsn: dsn}
}

// MySQL starts mysql:8.4 as root, or skips the test when Docker is unreachable.
func MySQL(t *testing.T) *Server {
	t.Helper()
	start(t)
	ctr, err := tcmysql.Run(t.Context(), "mysql:8.4", tcmysql.WithUsername("root"), tcmysql.WithPassword(rand.Text()))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	base, err := ctr.ConnectionString(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := mysqldriver.ParseDSN(base)
	if err != nil {
		t.Fatal(err)
	}
	dsn := func(name string) string {
		c := cfg.Clone()
		c.DBName = name
		return c.FormatDSN()
	}
	admin, err := sql.Open("mysql", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	return &Server{Admin: admin, dsn: dsn}
}
