// Package store holds the dialect-independent handle to the database pair.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strings"

	"github.com/uptrace/bun"
)

// Store pairs the single-connection write pool with the read pool.
type Store struct {
	Write, Read *bun.DB
	// SQL holds the dialect's files under "sql/<name>.sql".
	SQL       fs.FS
	IsIOError func(error) bool
	IsUnique  func(error) bool
}

func (s *Store) query(name string) (string, error) {
	b, err := fs.ReadFile(s.SQL, "sql/"+name+".sql")
	if err != nil {
		return "", fmt.Errorf("load sql %q: %w", name, err)
	}
	return string(b), nil
}

// Exec runs the named statement with positional arguments.
func (s *Store) Exec(ctx context.Context, db bun.IDB, name string, args ...any) (sql.Result, error) {
	q, err := s.query(name)
	if err != nil {
		return nil, err
	}
	return db.NewRaw(q, args...).Exec(ctx)
}

// Select runs the named query and scans the rows into dest.
func (s *Store) Select(ctx context.Context, db bun.IDB, dest any, name string, args ...any) error {
	q, err := s.query(name)
	if err != nil {
		return err
	}
	return db.NewRaw(q, args...).Scan(ctx, dest)
}

// Migrate runs schema.sql one statement at a time. The schema is idempotent
// (IF NOT EXISTS), and each statement ends with ";" at the end of a line.
func (s *Store) Migrate(ctx context.Context) error {
	schema, err := s.query("schema")
	if err != nil {
		return err
	}
	for _, stmt := range splitStatements(schema) {
		if _, err := s.Write.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func splitStatements(schema string) []string {
	var out []string
	var cur strings.Builder
	for line := range strings.SplitSeq(schema, "\n") {
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(strings.TrimSpace(line), ";") {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if rest := strings.TrimSpace(cur.String()); rest != "" {
		out = append(out, rest)
	}
	return out
}
