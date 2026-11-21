//go:build !unix

package engine

import (
	"context"
	"errors"
)

// AcquireLock is not available here: quietbox needs file locking to be safe.
func AcquireLock(string) (func(), error) {
	return nil, errors.New("quietbox can only run on Unix-like systems (it needs file locking)")
}

// AcquireLockWait is not available here either.
func AcquireLockWait(context.Context, string, func()) (func(), error) {
	return nil, errors.New("quietbox can only run on Unix-like systems (it needs file locking)")
}
