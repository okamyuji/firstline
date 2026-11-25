package sqlite

import (
	"context"
	"database/sql"
)

func OpenRaw(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", ReadDSN(path))
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func PingRaw(ctx context.Context, dsn string) error {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.PingContext(ctx)
}

var IsIOError = isIOError
