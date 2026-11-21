package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLockExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := AcquireLock(path); err == nil || !strings.Contains(err.Error(), "another quietbox") {
		t.Fatalf("second acquire: err = %v", err)
	}
}

func TestLockReleasedAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	release2()
}

func TestAcquireLockWaitWaitsThenSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		held()
	}()
	start := time.Now()
	release, err := AcquireLockWait(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("returned after %v, before the holder released", d)
	}
}

func TestAcquireLockWaitCallsOnWaitOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1600 * time.Millisecond)
		held()
	}()
	var n atomic.Int32
	release, err := AcquireLockWait(context.Background(), path, func() { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	release()
	if n.Load() != 1 {
		t.Fatalf("onWait called %d times, want 1", n.Load())
	}

	// A free lock never calls onWait.
	n.Store(0)
	release, err = AcquireLockWait(context.Background(), path, func() { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	release()
	if n.Load() != 0 {
		t.Fatalf("onWait called %d times on a free lock", n.Load())
	}
}

func TestAcquireLockWaitCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = AcquireLockWait(ctx, path, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("cancel took %v", d)
	}
}
