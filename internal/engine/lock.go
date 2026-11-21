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

// AcquireDaemonLock takes the lock that `quietbox run` holds for its whole
// life, failing at once if another run has it. It is a separate file from the
// per-pass lock (see DaemonLockPath) so that releasing the pass lock between
// passes never lets a second run start. Like every flock here it is dropped by
// the kernel when the process exits, however it exits (kill -9 included):
// nothing is written to the file, and its existence means nothing.
func AcquireDaemonLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another quietbox run is already running on this mailbox (lock file %s)", path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// AcquireLockWait is AcquireLock that waits for the lock instead of failing:
// it polls every 200 ms, calls onWait (if not nil) once after a second of
// waiting, and returns ctx.Err() as soon as ctx is cancelled.
func AcquireLockWait(ctx context.Context, path string, onWait func()) (release func(), err error) {
	return acquireLockPoll(ctx, path, 0, onWait)
}

// AcquireLockTimeout is AcquireLockWait that gives up after timeout, with an
// error that says how long it waited. ctx cancellation still returns ctx.Err().
func AcquireLockTimeout(ctx context.Context, path string, timeout time.Duration, onWait func()) (release func(), err error) {
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	return acquireLockPoll(ctx, path, timeout, onWait)
}

// acquireLockPoll is the one poll loop behind AcquireLockWait (timeout 0: wait
// forever) and AcquireLockTimeout. The file is closed on every failure path.
func acquireLockPoll(ctx context.Context, path string, timeout time.Duration, onWait func()) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	var expired <-chan time.Time // nil (never fires) when there is no timeout
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
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
		case <-expired:
			_ = f.Close()
			return nil, fmt.Errorf("another quietbox is still running on this mailbox after waiting %s (lock file %s)", timeout, path)
		case <-time.After(lockPoll):
		}
	}
}
