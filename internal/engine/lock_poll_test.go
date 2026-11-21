//go:build unix

package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// These tests drive the unix poll loop directly (its poll interval is not
// part of the portable API).

// A lock freed after the last poll but before the timeout fires must be
// taken, not reported as a timeout: the loop tries once more at expiry.
func TestAcquireLockTimeoutTriesOnceMoreAtExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		held()
	}()
	// One poll at the start, the next one far beyond the timeout: only the
	// attempt at expiry can see the lock free.
	release, err := acquireLockPoll(context.Background(), path, pollOpts{timeout: time.Second, poll: time.Minute})
	if err != nil {
		t.Fatalf("lock freed before the timeout: %v", err)
	}
	release()
}
