// Package postgres opens the PostgreSQL store.
package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"strings"

	"github.com/okamyuji/firstline/internal/store"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

//go:embed sql/*.sql
var sqlFiles embed.FS

const (
	readConns = 8

	uniqueViolation = "23505"
	// Class 57P is the server shutting down or crashing under the session, which
	// also ends the session after an fsync failure.
	operatorIntervention = "57P"
)

// Open opens the write and read pools on dsn and fails unless commits wait for the disk.
func Open(ctx context.Context, dsn string) (*store.Store, error) {
	// OpenConnector reports a malformed DSN as an error; pgdriver.WithDSN would panic.
	c, err := pgdriver.NewDriver().OpenConnector(dsn)
	if err != nil {
		// The parse error quotes the DSN (net/url *url.Error), password included.
		return nil, errors.New("open postgres: malformed DSN")
	}
	s := &store.Store{Write: pool(c, 1), Read: pool(c, readConns), SQL: sqlFiles, IsIOError: isIOError, IsUnique: isUnique}
	if err := s.CheckDurability(ctx); err != nil {
		_ = s.Write.Close()
		_ = s.Read.Close()
		return nil, err
	}
	return s, nil
}

func pool(c driver.Connector, conns int) *bun.DB {
	sqlDB := sql.OpenDB(c)
	sqlDB.SetMaxOpenConns(conns)
	sqlDB.SetMaxIdleConns(conns)
	return bun.NewDB(sqlDB, pgdialect.New())
}

func sqlState(err error) string {
	e, _ := errors.AsType[pgdriver.Error](err)
	return e.Field('C')
}

func isUnique(err error) bool { return sqlState(err) == uniqueViolation }

func isIOError(err error) bool {
	return store.IsConnLost(err) || strings.HasPrefix(sqlState(err), operatorIntervention)
}
