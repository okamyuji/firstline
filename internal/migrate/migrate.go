// Package migrate copies every table from one store into an empty one.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"

	"github.com/okamyuji/firstline/internal/store"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Tables lists every table in the order Run copies them.
var Tables = []string{
	"organizers", "sessions", "events", "event_sales", "purchases",
	"orphan_refunds", "mail_outbox", "billing_logs", "access_logs",
}

type totals struct{ rows, amount int64 }

// Run copies every table from from into the empty to in one transaction, so a
// failure leaves to empty. It compares each table's row count and amount sum
// and returns the first mismatch. On PostgreSQL it then moves the mail_outbox
// id sequence past the copied ids, because inserting explicit ids leaves the
// sequence where it was.
func Run(ctx context.Context, from, to *store.Store) error {
	return to.Write.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := requireEmpty(ctx, to, tx); err != nil {
			return err
		}
		var mail int64
		for _, t := range Tables {
			n, err := copyTable(ctx, from, to, tx, t)
			if err != nil {
				return err
			}
			if t == "mail_outbox" {
				mail = n
			}
		}
		if mail > 0 && to.Write.Dialect().Name() == dialect.PG {
			if _, err := to.Exec(ctx, tx, "setval_mail_outbox"); err != nil {
				return fmt.Errorf("setval mail_outbox: %w", err)
			}
		}
		return nil
	})
}

func requireEmpty(ctx context.Context, to *store.Store, tx bun.Tx) error {
	for _, t := range Tables {
		got, err := tally(ctx, to, tx, t, nil)
		if err != nil {
			return err
		}
		if got.rows > 0 {
			return fmt.Errorf("target table %s has %d rows; migrate-data needs an empty target", t, got.rows)
		}
	}
	return nil
}

// copyTable copies one table and returns its row count once the target matches the source.
func copyTable(ctx context.Context, from, to *store.Store, tx bun.Tx, t string) (int64, error) {
	want, err := tally(ctx, from, from.Read, t, func(row []any) error {
		_, err := to.Exec(ctx, tx, "import_"+t, row...)
		return err
	})
	if err != nil {
		return 0, err
	}
	got, err := tally(ctx, to, tx, t, nil)
	if err != nil {
		return 0, err
	}
	if got.rows != want.rows {
		return 0, fmt.Errorf("%s: %d rows in the target, %d in the source", t, got.rows, want.rows)
	}
	if got.amount != want.amount {
		return 0, fmt.Errorf("%s: amount sum %d in the target, %d in the source", t, got.amount, want.amount)
	}
	return got.rows, nil
}

// tally reads every row of table through export_<table>, passes each to each
// when it is not nil, and returns the row count and the sum of the amount
// column (zero for tables without one).
func tally(ctx context.Context, s *store.Store, db bun.IDB, table string, each func([]any) error) (totals, error) {
	var t totals
	rows, err := s.Query(ctx, db, "export_"+table)
	if err != nil {
		return t, fmt.Errorf("read %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return t, fmt.Errorf("read %s: %w", table, err)
	}
	amount := slices.Index(cols, "amount")
	for rows.Next() {
		row, n, err := scan(rows, len(cols), amount)
		if err != nil {
			return t, fmt.Errorf("read %s: %w", table, err)
		}
		t.rows++
		t.amount += n
		if each == nil {
			continue
		}
		if err := each(row); err != nil {
			return t, fmt.Errorf("write %s: %w", table, err)
		}
	}
	if err := rows.Err(); err != nil {
		return t, fmt.Errorf("read %s: %w", table, err)
	}
	return t, nil
}

// scan reads the current row and the integer at column amount (zero when amount is -1).
func scan(rows *sql.Rows, ncols, amount int) ([]any, int64, error) {
	row := make([]any, ncols)
	ptrs := make([]any, ncols)
	for i := range row {
		ptrs[i] = &row[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, 0, err
	}
	if amount == -1 {
		return row, 0, nil
	}
	n, err := asInt(row[amount])
	return row, n, err
}

// asInt reads an integer column: MySQL's text protocol returns it as bytes.
func asInt(v any) (int64, error) {
	switch v := v.(type) {
	case int64:
		return v, nil
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	}
	return 0, fmt.Errorf("amount is %T, want an integer", v)
}
