package cli

import (
	"bytes"
	"context"
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
	dir := t.TempDir()
	_, stderr := runMigrate(t, filepath.Join(dir, "l"), "--from", "oracle:user:secretpw@host", "--to", "sqlite:"+filepath.Join(dir, "x.db"))
	if strings.Contains(stderr, "secretpw") {
		t.Errorf("stderr leaks the DSN: %s", stderr)
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
