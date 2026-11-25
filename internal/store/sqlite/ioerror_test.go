package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fsqlite "github.com/okamyuji/firstline/internal/store/sqlite"
	"github.com/okamyuji/firstline/internal/store/writer"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	msqlite "modernc.org/sqlite"
	"modernc.org/sqlite/vfs"
)

// memFS is an in-memory vfs.VFS whose Sync fails while fail is set, which
// SQLite reports as SQLITE_IOERR_FSYNC.
type memFS struct {
	mu    sync.Mutex
	files map[string]*memFile
	fail  atomic.Bool
}

type memFile struct {
	fs *memFS
	b  []byte
}

func (m *memFS) Open(name string, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.files[name]
	if f == nil {
		if flags&vfs.OpenCreate == 0 {
			return nil, 0, fs.ErrNotExist
		}
		f = &memFile{fs: m}
		m.files[name] = f
	}
	return f, flags, nil
}

func (m *memFS) Delete(name string, _ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.files[name] == nil {
		return fs.ErrNotExist
	}
	delete(m.files, name)
	return nil
}

func (m *memFS) Access(name string, _ vfs.AccessFlag) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.files[name] != nil, nil
}

func (m *memFS) FullPathname(name string) (string, error) { return name, nil }

func (f *memFile) ReadAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if off >= int64(len(f.b)) {
		return 0, io.EOF
	}
	n := copy(p, f.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *memFile) WriteAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if end := off + int64(len(p)); end > int64(len(f.b)) {
		f.b = append(f.b, make([]byte, end-int64(len(f.b)))...)
	}
	return copy(f.b[off:], p), nil
}

func (f *memFile) Truncate(size int64) error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if size < int64(len(f.b)) {
		f.b = f.b[:size]
	} else {
		f.b = append(f.b, make([]byte, size-int64(len(f.b)))...)
	}
	return nil
}

func (f *memFile) Sync(vfs.SyncFlag) error {
	if f.fs.fail.Load() {
		return errors.New("fsync failed")
	}
	return nil
}

func (f *memFile) Size() (int64, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	return int64(len(f.b)), nil
}

func (f *memFile) Close() error { return nil }

func TestFailingSyncIsFatalToTheWriter(t *testing.T) {
	ctx := t.Context()
	m := &memFS{files: map[string]*memFile{}}
	name, fsys, err := vfs.Register(m)
	if err != nil {
		t.Fatal(err)
	}
	// A registered VFS has no shared memory, so this uses the rollback journal on one connection.
	sqldb, err := sql.Open("sqlite", "file:io.db?vfs="+name+"&_pragma=synchronous(FULL)")
	if err != nil {
		t.Fatal(err)
	}
	sqldb.SetMaxOpenConns(1)
	db := bun.NewDB(sqldb, sqlitedialect.New())
	t.Cleanup(func() { _ = db.Close(); _ = fsys.Close() })
	if _, err := db.ExecContext(ctx, "CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	m.fail.Store(true)

	_, err = db.ExecContext(ctx, "INSERT INTO t VALUES (1)")
	if e, ok := errors.AsType[*msqlite.Error](err); !ok || e.Code() != 1034 || !fsqlite.IsIOError(err) {
		t.Fatalf("insert with failing fsync = %v, IsIOError = %v; want SQLITE_IOERR_FSYNC (1034) classified as I/O", err, fsqlite.IsIOError(err))
	}

	fatal := make(chan error, 1)
	w, err := writer.New(db, writer.Options{
		QueueCap: 8, BatchMax: 64, BatchWait: time.Millisecond,
		IsIOError:    fsqlite.IsIOError,
		OnFatal:      func(err error) { fatal <- err },
		Now:          time.Now,
		After:        time.After,
		OnSyncResult: func(time.Time, time.Duration) {},
		OnAsyncError: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := make(chan error, 1)
	// This Do never returns: a writer that hit an I/O error answers no caller.
	go func() {
		reply <- w.Do(context.Background(), func(ctx context.Context, tx bun.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO t VALUES (2)")
			return err
		})
	}()
	done := make(chan struct{})
	go func() { w.Run(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not stop after the I/O error")
	}
	select {
	case err := <-fatal:
		if !fsqlite.IsIOError(err) {
			t.Errorf("OnFatal(%v), want an I/O error", err)
		}
	default:
		t.Fatal("OnFatal was not called")
	}
	select {
	case err := <-reply:
		t.Errorf("caller got reply %v, want none", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// SQLite rolls back the whole transaction on SQLITE_FULL, so the savepoint is
// gone; the items must still see the full-disk error, not only "no such savepoint".
func TestAutoRollbackKeepsTheItemError(t *testing.T) {
	s := open(t)
	if _, err := s.Write.ExecContext(t.Context(), "CREATE TABLE blobs (b BLOB)"); err != nil {
		t.Fatal(err)
	}
	w, err := writer.New(s.Write, writer.Options{
		QueueCap: 8, BatchMax: 64, BatchWait: time.Millisecond,
		IsIOError:    s.IsIOError,
		OnFatal:      func(err error) { t.Errorf("OnFatal(%v) for a full disk", err) },
		Now:          time.Now,
		After:        time.After,
		OnSyncResult: func(time.Time, time.Duration) {},
		OnAsyncError: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	go w.Run()
	t.Cleanup(w.Stop)
	err = w.Do(t.Context(), func(ctx context.Context, tx bun.Tx) error {
		var pages int
		if err := tx.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA max_page_count = "+strconv.Itoa(pages+1)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO blobs VALUES (randomblob(1048576))")
		return err
	})
	if e, ok := errors.AsType[*msqlite.Error](err); !ok || e.Code()&0xff != 13 {
		t.Fatalf("Do = %v, want the SQLITE_FULL (13) error kept", err)
	}
}
