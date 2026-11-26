package cli

import (
	"fmt"
	"os"
	"syscall"
)

// acquireLock takes a non-blocking exclusive flock on path. The lock lasts
// until the returned file is closed, so the caller keeps it referenced for as
// long as it writes; a collected *os.File would close the descriptor.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s (is another firstline process running?): %w", path, err)
	}
	return f, nil
}
