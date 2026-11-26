package cli

import (
	"path/filepath"
	"testing"
)

func TestLockIsExclusiveUntilClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "firstline.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireLock(path); err == nil {
		_ = second.Close()
		t.Fatal("second acquireLock succeeded while the first holds the lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireLock(path)
	if err != nil {
		t.Fatalf("acquireLock after release: %v", err)
	}
	_ = again.Close()
}

func TestLockFailsWhenFileCannotBeOpened(t *testing.T) {
	if f, err := acquireLock(filepath.Join(t.TempDir(), "missing", "x.lock")); err == nil {
		_ = f.Close()
		t.Fatal("acquireLock in a missing directory succeeded")
	}
}
