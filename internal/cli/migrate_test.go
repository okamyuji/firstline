package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/firstline/internal/store/sqlite"
)

func runMigrate(t *testing.T, lock string, args ...string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	code := run(context.Background(), append([]string{"migrate-data"}, args...),
		testEnv(map[string]string{"FIRSTLINE_LOCK_PATH": lock}, strings.NewReader(""), &stderr))
	return code, stderr.String()
}

func TestMigrateDataCopiesSQLiteToSQLite(t *testing.T) {
	dir := t.TempDir()
	src, dst, lock := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db"), filepath.Join(dir, "l")
	over := map[string]string{"FIRSTLINE_DB_PATH": src, "FIRSTLINE_LOCK_PATH": lock, "FIRSTLINE_ADMIN_SOCK": filepath.Join(dir, "none.sock")}
	if code, stderr := runCreate(t, over, "long enough pw\n"); code != 0 {
		t.Fatalf("seed organizer: exit %d (%s)", code, stderr)
	}
	if code, stderr := runMigrate(t, lock, "--from", "sqlite:"+src, "--to", "sqlite:"+dst); code != 0 {
		t.Fatalf("exit = %d; want 0 (%s)", code, stderr)
	}
	st, err := sqlite.Open(t.Context(), dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeStore(st) }()
	var n int
	if err := st.Read.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM organizers").Scan(&n); err != nil || n != 1 {
		t.Errorf("organizers in target = %d, %v; want 1", n, err)
	}
	if code, _ := runMigrate(t, lock, "--from", "sqlite:"+src, "--to", "sqlite:"+dst); code != 1 {
		t.Errorf("second run into a non-empty target: exit = %d; want 1", code)
	}
}

func TestMigrateDataRejectsBadStores(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "l")
	good := "sqlite:" + filepath.Join(dir, "ok.db")
	for _, args := range [][]string{
		{"--from", "oracle:x", "--to", good},
		{"--from", good, "--to", "nocolon"},
		{"--from", "postgres:://bad", "--to", good},
		{"--from", good, "--to", "mysql:bad dsn"},
		{"--from", "sqlite:" + filepath.Join(dir, "missing", "x.db"), "--to", good},
		{"--bogus"},
	} {
		if code, _ := runMigrate(t, lock, args...); code != 1 {
			t.Errorf("migrate-data %q: exit = %d; want 1", args, code)
		}
	}
}

func TestMigrateDataErrorHidesDSN(t *testing.T) {
	for _, spec := range []string{
		"oracle:user:secretpw@host",
		"postgres:postgres://user:secretpw@host:badport/db",
		"mysql:user:secretpw@tcp(host:3306/db",
		"mysql:user:secretpw@tcp(host:3306)/db?parseTime=secretpw",
		// Well-formed DSNs whose server refuses the connection.
		"postgres:postgres://user:secretpw@127.0.0.1:1/db?sslmode=disable",
		"mysql:user:secretpw@tcp(127.0.0.1:1)/db",
	} {
		dir := t.TempDir()
		code, stderr := runMigrate(t, filepath.Join(dir, "l"), "--from", spec, "--to", "sqlite:"+filepath.Join(dir, "x.db"))
		if code != 1 || strings.Contains(stderr, "secretpw") || strings.Contains(stderr, "host") {
			t.Errorf("%s: exit %d, stderr leaks the DSN: %s", spec, code, stderr)
		}
	}
}

func TestMigrateDataNeedsTheLock(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "l")
	held, err := acquireLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	if code, _ := runMigrate(t, lock, "--from", "sqlite:"+filepath.Join(dir, "a.db"), "--to", "sqlite:"+filepath.Join(dir, "b.db")); code != 1 {
		t.Errorf("exit = %d; want 1", code)
	}
}

func TestMigrateDataMissingSourceCreatesNoFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "typo.db")
	code, stderr := runMigrate(t, filepath.Join(dir, "l"), "--from", "sqlite:"+src, "--to", "sqlite:"+filepath.Join(dir, "x.db"))
	if code != 1 || !strings.Contains(stderr, "no such file") {
		t.Errorf("exit %d, stderr %q; want 1 and a missing-file error", code, stderr)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("the missing source was created: %v", err)
	}
}
