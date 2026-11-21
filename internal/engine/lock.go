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
// needed), failing at once if it is held. The lock is released by the returned
// function, or by the OS if the process dies. Only tests and test helpers call
// it: production code waits for the pass lock (AcquireLockWait,
// AcquireLockWaitNotify, AcquireLockTimeout) or fails fast on the daemon lock
// (AcquireDaemonLock).
func AcquireLock(path string) (release func(), err error) {
	return acquireNow(path, "another quietbox is already running on this mailbox (lock file %s): wait for it or stop it first")
}

// acquireNow is the fail-fast flock; busyFmt is the error for a held lock and
// takes the path.
func acquireNow(path, busyFmt string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf(busyFmt, path)
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
	return acquireNow(path, "another quietbox run is already running on this mailbox (lock file %s)")
}

// AcquireLockWait is AcquireLock that waits for the lock instead of failing:
// it polls every 200 ms, calls onWait (if not nil) once after a second of
// waiting, and returns ctx.Err() as soon as ctx is cancelled.
func AcquireLockWait(ctx context.Context, path string, onWait func()) (release func(), err error) {
	return acquireLockPoll(ctx, path, pollOpts{poll: lockPoll, onWait: once(onWait)})
}

// AcquireLockWaitNotify is AcquireLockWait whose notice repeats: onWait gets
// the time waited so far after a second and then every renotify after that
// (renotify <= 0: once only). The wait itself is unbounded.
func AcquireLockWaitNotify(ctx context.Context, path string, renotify time.Duration, onWait func(waited time.Duration)) (release func(), err error) {
	return acquireLockPoll(ctx, path, pollOpts{poll: lockPoll, renotify: renotify, onWait: onWait})
}

// AcquireLockTimeout is AcquireLockWait that gives up after timeout, with an
// error that says how long it waited. ctx cancellation still returns ctx.Err().
func AcquireLockTimeout(ctx context.Context, path string, timeout time.Duration, onWait func()) (release func(), err error) {
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	return acquireLockPoll(ctx, path, pollOpts{timeout: timeout, poll: lockPoll, onWait: once(onWait)})
}

// once adapts a no-argument notice to the poll loop's notifier (nil stays nil).
func once(f func()) func(time.Duration) {
	if f == nil {
		return nil
	}
	return func(time.Duration) { f() }
}

// pollOpts configures acquireLockPoll.
type pollOpts struct {
	timeout  time.Duration // 0: wait forever
	poll     time.Duration // how often LOCK_NB is retried
	renotify time.Duration // repeat onWait this often after the first notice; 0: once only
	onWait   func(waited time.Duration)
}

// acquireLockPoll is the one poll loop behind AcquireLockWait,
// AcquireLockWaitNotify and AcquireLockTimeout. The file is closed on every
// failure path.
func acquireLockPoll(ctx context.Context, path string, o pollOpts) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	var expired <-chan time.Time // nil (never fires) when there is no timeout
	if o.timeout > 0 {
		timer := time.NewTimer(o.timeout)
		defer timer.Stop()
		expired = timer.C
	}
	start := time.Now()
	next := lockWaitNotice // elapsed time at which onWait is due next; <0: never again
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
		if waited := time.Since(start); o.onWait != nil && next >= 0 && waited >= next {
			o.onWait(waited)
			if o.renotify > 0 {
				for next <= waited {
					next += o.renotify
				}
			} else {
				next = -1
			}
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-expired:
			if err := ctx.Err(); err != nil { // cancellation wins over a simultaneous timeout
				_ = f.Close()
				return nil, err
			}
			// One last try: a lock freed since the last poll is taken, not
			// reported as a timeout.
			if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				return func() {
					_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					_ = f.Close()
				}, nil
			}
			_ = f.Close()
			return nil, fmt.Errorf("another quietbox is still running on this mailbox after waiting %s (lock file %s)", o.timeout, path)
		case <-time.After(o.poll):
		}
	}
}
