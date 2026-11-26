// Package mysql opens the MySQL store.
package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/okamyuji/firstline/internal/store"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/mysqldialect"
)

//go:embed sql/*.sql
var sqlFiles embed.FS

const (
	readConns = 8

	errDupEntry = 1062
)

// Open opens the write and read pools on dsn and fails unless commits wait for the disk.
func Open(ctx context.Context, dsn string) (*store.Store, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	var c driver.Connector
	if err == nil {
		// Changed-row counts keep insert_orphan's duplicate at 0 rows, like ON CONFLICT DO NOTHING elsewhere.
		cfg.ClientFoundRows = false
		c, err = mysqldriver.NewConnector(cfg)
	}
	if err != nil {
		// ParseDSN errors can quote a parameter value from the DSN.
		return nil, errors.New("open mysql: malformed DSN")
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
	return bun.NewDB(sqlDB, mysqldialect.New())
}

func isUnique(err error) bool {
	e, ok := errors.AsType[*mysqldriver.MySQLError](err)
	return ok && e.Number == errDupEntry
}

func isIOError(err error) bool {
	return store.IsConnLost(err) || errors.Is(err, mysqldriver.ErrInvalidConn)
}
