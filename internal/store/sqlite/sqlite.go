// Package sqlite opens the SQLite store with separate write and read pools.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/okamyuji/firstline/internal/store"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	msqlite "modernc.org/sqlite"
)

//go:embed sql/*.sql
var sqlFiles embed.FS

const (
	readConns = 8

	// Primary result code and the extended codes (primary | subcode<<8).
	codeIOErr      = 10
	codeUnique     = 2067
	codePrimaryKey = 1555
)

// WriteDSN opens the path read-write in WAL. The path comes from the caller's
// own configuration; the rest is constant because _pragma values run as SQL.
func WriteDSN(path string) string {
	return "file:" + path + "?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=1&_txlock=immediate&_pragma=wal_autocheckpoint(0)&_pragma=cache_size(-16384)"
}

// ReadDSN opens the path read-only. It must start with "file:" or the driver
// ignores mode=ro.
func ReadDSN(path string) string {
	return "file:" + path + "?mode=ro&_busy_timeout=5000&_pragma=mmap_size(268435456)&_pragma=cache_size(-65536)"
}

// Open opens the write pool first and pings it so the file is in WAL before
// the read-only pool opens: a mode=ro connection cannot switch the journal mode.
func Open(ctx context.Context, path string) (*store.Store, error) {
	msqlite.StrictPragmas(true)

	w, err := openPool(ctx, WriteDSN(path), 1)
	if err != nil {
		return nil, fmt.Errorf("open write db: %w", err)
	}
	s := &store.Store{Write: w, SQL: sqlFiles, IsIOError: isIOError, IsUnique: isUnique}
	r, err := openPool(ctx, ReadDSN(path), readConns)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open read db: %w", err)
	}
	s.Read = r
	return s, nil
}

func openPool(ctx context.Context, dsn string, conns int) (*bun.DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(conns)
	sqlDB.SetMaxIdleConns(conns)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return bun.NewDB(sqlDB, sqlitedialect.New()), nil
}

func code(err error) (int, bool) {
	e, ok := errors.AsType[*msqlite.Error](err)
	if !ok {
		return 0, false
	}
	return e.Code(), true
}

func isIOError(err error) bool {
	c, ok := code(err)
	return ok && c&0xff == codeIOErr
}

func isUnique(err error) bool {
	c, ok := code(err)
	return ok && (c == codeUnique || c == codePrimaryKey)
}
