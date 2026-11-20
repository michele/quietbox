//go:build unix

package engine

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// AcquireLock takes an exclusive, non-blocking lock on path (created if
// needed) so that two quietbox instances never act on one mailbox at once. The
// lock is released by the returned function, or by the OS if the process dies.
func AcquireLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another quietbox is already running on this mailbox (lock file %s): wait for it or stop it first", path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
