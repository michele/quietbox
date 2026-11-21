//go:build unix

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
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

const (
	lockPoll       = 200 * time.Millisecond
	lockWaitNotice = time.Second
)

// AcquireLockWait is AcquireLock that waits for the lock instead of failing:
// it polls every 200 ms, calls onWait (if not nil) once after a second of
// waiting, and returns ctx.Err() as soon as ctx is cancelled.
func AcquireLockWait(ctx context.Context, path string, onWait func()) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	start := time.Now()
	notified := false
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if !notified && onWait != nil && time.Since(start) >= lockWaitNotice {
			notified = true
			onWait()
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
