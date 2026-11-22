package engine

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/store"
)

// holdPassLock takes the pass lock on another file description (flock treats
// it like another process) and returns its release, safe to call twice.
func holdPassLock(t *testing.T, path string) func() {
	t.Helper()
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	var once atomic.Bool
	done := func() {
		if once.CompareAndSwap(false, true) {
			release()
		}
	}
	t.Cleanup(done)
	return done
}

// releaseAfter releases a held lock from another goroutine after d.
func releaseAfter(d time.Duration, release func()) {
	go func() {
		time.Sleep(d)
		release()
	}()
}

func TestLockTimeoutDefault(t *testing.T) {
	if DefaultLockTimeout != 2*time.Minute {
		t.Errorf("DefaultLockTimeout = %v, want 2m", DefaultLockTimeout)
	}
	for in, want := range map[time.Duration]time.Duration{0: 2 * time.Minute, -time.Second: 2 * time.Minute, 3 * time.Second: 3 * time.Second} {
		if got := lockTimeout(in); got != want {
			t.Errorf("lockTimeout(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestProcessWaitsForPassLockThenRuns(t *testing.T) {
	fake, st, cfg := processBox(t)
	releaseAfter(500*time.Millisecond, holdPassLock(t, LockPath(cfg)))
	var fetches, unlocked atomic.Int32
	fake.OnFetch = func(string) {
		fetches.Add(1)
		if lockFree(LockPath(cfg)) { // Process must hold it while it works
			unlocked.Add(1)
		}
	}
	start := time.Now()
	rep, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 400*time.Millisecond {
		t.Errorf("Process ran after %v: it did not wait for the held lock", waited)
	}
	if fetches.Load() == 0 || unlocked.Load() != 0 {
		t.Errorf("fetches = %d, fetches without the pass lock held = %d", fetches.Load(), unlocked.Load())
	}
	if len(rep.Moves) != 7 || fake.MoveCount() != 7 {
		t.Errorf("moves = %d (server %d), want 7", len(rep.Moves), fake.MoveCount())
	}
	if !lockFree(LockPath(cfg)) {
		t.Error("Process kept the pass lock after returning")
	}
}

func TestProcessLockTimeout(t *testing.T) {
	fake, st, cfg := processBox(t)
	holdPassLock(t, LockPath(cfg))
	var waits atomic.Int32
	start := time.Now()
	_, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow, LockTimeout: 400 * time.Millisecond, OnLockWait: func() { waits.Add(1) }})
	if err == nil || !strings.Contains(err.Error(), "after waiting 400ms") || !strings.Contains(err.Error(), LockPath(cfg)) {
		t.Fatalf("err = %v", err)
	}
	if waited := time.Since(start); waited < 400*time.Millisecond {
		t.Errorf("gave up after %v, before the timeout", waited)
	}
	if fake.MoveCount() != 0 {
		t.Error("moved mail without the pass lock")
	}
	if waits.Load() != 0 {
		t.Errorf("OnLockWait called %d times before a second of waiting", waits.Load())
	}
}

func TestUndoWaitsForPassLockThenRuns(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	releaseAfter(1800*time.Millisecond, holdPassLock(t, LockPath(cfg)))
	var moves, unlocked, waits atomic.Int32
	fake.OnMove = func(string, string, uint32) {
		moves.Add(1)
		if lockFree(LockPath(cfg)) {
			unlocked.Add(1)
		}
	}
	start := time.Now()
	rep, err := Undo(bg, cfg, fake, st, UndoOptions{Now: undoNow, Since: 2 * time.Hour, LockTimeout: 5 * time.Second, OnLockWait: func() { waits.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < time.Second {
		t.Errorf("Undo ran after %v: it did not wait for the held lock", waited)
	}
	if countStatus(rep, UndoRestored) != 7 || moves.Load() != 7 || unlocked.Load() != 0 {
		t.Errorf("restored = %d, moves = %d, moves without the pass lock held = %d", countStatus(rep, UndoRestored), moves.Load(), unlocked.Load())
	}
	if waits.Load() != 1 {
		t.Errorf("OnLockWait called %d times, want once after a second", waits.Load())
	}
}

func TestUndoLockTimeout(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	holdPassLock(t, LockPath(cfg))
	before := fake.MoveCount()
	_, err := Undo(bg, cfg, fake, st, UndoOptions{Now: undoNow, Since: 2 * time.Hour, LockTimeout: 400 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "after waiting 400ms") {
		t.Fatalf("err = %v", err)
	}
	if fake.MoveCount() != before {
		t.Error("undo moved mail without the pass lock")
	}
	if _, err := Undo(bg, cfg, fake, st, UndoOptions{Now: undoNow, Since: 2 * time.Hour, DryRun: true, LockTimeout: 5 * time.Second}); err != nil {
		t.Fatalf("a dry-run undo should not need the lock: %v", err)
	}
}

func TestLearnTakesThePassLock(t *testing.T) {
	t.Run("a real run waits and times out while the lock is held", func(t *testing.T) {
		cfg := learnConfig(t, "")
		holdPassLock(t, LockPath(cfg))
		fake, st := miniMailbox(), openStore(t)
		var fetches atomic.Int32
		fake.OnFetch = func(string) { fetches.Add(1) }
		start := time.Now()
		_, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow, Yes: true, LockTimeout: 400 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "after waiting 400ms") {
			t.Fatalf("err = %v", err)
		}
		if waited := time.Since(start); waited < 400*time.Millisecond {
			t.Errorf("gave up after %v, before the timeout", waited)
		}
		if fetches.Load() != 0 || len(fake.Created) != 0 {
			t.Errorf("scanned %d folders / created %v without the pass lock", fetches.Load(), fake.Created)
		}
		if rows, _ := st.Senders(context.Background(), store.SenderFilter{}); len(rows) != 0 {
			t.Errorf("wrote %d senders without the pass lock", len(rows))
		}
	})
	t.Run("a dry run does not wait", func(t *testing.T) {
		cfg := learnConfig(t, "")
		holdPassLock(t, LockPath(cfg))
		start := time.Now()
		if _, err := Learn(context.Background(), cfg, miniMailbox(), nil, LearnOptions{Now: learnNow, DryRun: true, LockTimeout: 5 * time.Second}); err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited > time.Second {
			t.Errorf("dry run took %v: it waited for the lock", waited)
		}
	})
	t.Run("a real run holds the lock for the whole scan", func(t *testing.T) {
		cfg := learnConfig(t, "")
		fake, st := miniMailbox(), openStore(t)
		var fetches, unlocked atomic.Int32
		fake.OnFetch = func(string) {
			fetches.Add(1)
			if lockFree(LockPath(cfg)) {
				unlocked.Add(1)
			}
		}
		if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow}); err != nil {
			t.Fatal(err)
		}
		if fetches.Load() == 0 || unlocked.Load() != 0 {
			t.Errorf("fetches = %d, fetches without the pass lock held = %d", fetches.Load(), unlocked.Load())
		}
		if !lockFree(LockPath(cfg)) {
			t.Error("Learn kept the pass lock after returning")
		}
	})
	t.Run("a scoped re-learn waits too", func(t *testing.T) {
		cfg := learnConfig(t, "")
		fake, st := miniMailbox(), openStore(t)
		if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow}); err != nil {
			t.Fatal(err)
		}
		holdPassLock(t, LockPath(cfg))
		_, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow, Folder: "SaneNews", LockTimeout: 300 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), "after waiting 300ms") {
			t.Fatalf("err = %v", err)
		}
	})
}
