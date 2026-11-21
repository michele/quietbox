//go:build !unix

package engine

import (
	"context"
	"errors"
	"time"
)

// AcquireLock is not available here: quietbox needs file locking to be safe.
func AcquireLock(string) (func(), error) {
	return nil, errors.New("quietbox can only run on Unix-like systems (it needs file locking)")
}

// AcquireLockWait is not available here either.
func AcquireLockWait(context.Context, string, func()) (func(), error) {
	return nil, errors.New("quietbox can only run on Unix-like systems (it needs file locking)")
}

// AcquireDaemonLock is not available here either.
func AcquireDaemonLock(string) (func(), error) {
	return nil, errors.New("quietbox can only run on Unix-like systems (it needs file locking)")
}

// AcquireLockTimeout is not available here either.
func AcquireLockTimeout(context.Context, string, time.Duration, func()) (func(), error) {
	return nil, errors.New("quietbox can only run on Unix-like systems (it needs file locking)")
}
