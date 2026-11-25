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

// query loads the named statement and checks it takes nargs arguments: bun
// leaves a missing argument as an unbound "?" (NULL in SQLite) and drops an
// extra one, so a wrong count would otherwise run silently.
func (s *Store) query(name string, nargs int) (string, error) {
	b, err := fs.ReadFile(s.SQL, "sql/"+name+".sql")
	if err != nil {
		return "", fmt.Errorf("load sql %q: %w", name, err)
	}
	q := string(b)
	if n := Placeholders(q); n != nargs {
		return "", fmt.Errorf("sql %q takes %d arguments, got %d", name, n, nargs)
	}
	return q, nil
}

// Placeholders counts the "?" outside quoted text and comments. bun replaces
// every "?" wherever it stands, so the SQL files keep "?" out of quotes and
// comments, and then this count is the number of arguments bun binds.
func Placeholders(q string) int {
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			continue
		}
		for _, p := range skipped {
			if strings.HasPrefix(q[i:], p[0]) {
				i = skipPast(q, i+len(p[0]), p[1])
				break
			}
		}
	}
	return n
}

// skipped pairs the start of quoted text or a comment with its end.
var skipped = [][2]string{{"'", "'"}, {`"`, `"`}, {"--", "\n"}, {"/*", "*/"}}

// skipPast returns the index of the last byte of the first end at or after
// from, or len(q) when there is none.
func skipPast(q string, from int, end string) int {
	j := strings.Index(q[from:], end)
	if j < 0 {
		return len(q)
	}
	return from + j + len(end) - 1
}

// Exec runs the named statement with positional arguments.
func (s *Store) Exec(ctx context.Context, db bun.IDB, name string, args ...any) (sql.Result, error) {
	q, err := s.query(name, len(args))
	if err != nil {
		return nil, err
	}
	return db.NewRaw(q, args...).Exec(ctx)
}

// Select runs the named query and scans the rows into dest.
func (s *Store) Select(ctx context.Context, db bun.IDB, dest any, name string, args ...any) error {
	q, err := s.query(name, len(args))
	if err != nil {
		return err
	}
	return db.NewRaw(q, args...).Scan(ctx, dest)
}

// Migrate runs schema.sql one statement at a time. The schema is idempotent
// (IF NOT EXISTS), and each statement ends with ";" at the end of a line.
// Lines starting with "--" are dropped, so a comment never ends a statement.
func (s *Store) Migrate(ctx context.Context) error {
	schema, err := s.query("schema", 0)
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
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
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
