package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
	"github.com/michele/quietbox/internal/store"
)

func fastBackoff(int) time.Duration { return time.Millisecond }

// startRun runs Run in a goroutine and returns a stop function that cancels it
// and returns Run's result.
func startRun(t *testing.T, cfg *config.Config, st *store.Store, o RunOptions) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, st, o) }()
	var once sync.Once
	var result error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case result = <-done:
			case <-time.After(5 * time.Second):
				t.Error("Run did not stop after cancel")
			}
		})
		return result
	}
	t.Cleanup(func() { _ = stop() })
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunProcessesOnWake(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Wake = make(chan struct{}, 1)
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) { return fake, nil },
		Backoff: fastBackoff,
	})
	waitFor(t, "the first pass", func() bool { return fake.CountFrom("SaneBulk", "noreply@shop.test") == 1 })

	fake.Append("INBOX", newMail("late@substack.test", 0, "<late>", nil))
	fake.Wake <- struct{}{}
	waitFor(t, "the pass after the wake-up", func() bool { return fake.CountFrom("SaneNews", "late@substack.test") == 1 })
	if err := stop(); err != nil {
		t.Fatalf("Run after cancel = %v, want nil", err)
	}
}

// flaky wraps a Fake and fails FetchFrom while failing is set, like a dropped connection.
type flaky struct {
	*mailtest.Fake
	failing atomic.Bool
	closed  atomic.Bool
}

func (f *flaky) FetchFrom(ctx context.Context, folder string, from uint32, fn func([]mail.Header) error) error {
	if f.failing.Load() {
		return errors.New("connection reset by peer")
	}
	return f.Fake.FetchFrom(ctx, folder, from, fn)
}

func (f *flaky) Close() error { f.closed.Store(true); return nil }

func TestRunReconnectsAfterError(t *testing.T) {
	fake, st, cfg := processBox(t)
	first := &flaky{Fake: fake}
	first.failing.Store(true)
	second := &flaky{Fake: fake}
	var calls atomic.Int32
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) {
			if calls.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
		Backoff: fastBackoff,
	})
	waitFor(t, "all mail filed after the reconnect", func() bool { return fake.MoveCount() == 7 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if !first.closed.Load() {
		t.Error("the broken connection was not closed")
	}
	if got := fake.MoveCount(); got != 7 { // every message exactly once
		t.Errorf("moves = %d, want 7", got)
	}
}

func TestRunRetriesFailedConnect(t *testing.T) {
	fake, st, cfg := processBox(t)
	var calls atomic.Int32
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) {
			if calls.Add(1) < 3 {
				return nil, errors.New("dial: connection refused")
			}
			return fake, nil
		},
		Backoff: fastBackoff,
	})
	waitFor(t, "mail filed after the third connect", func() bool { return fake.CountFrom("SaneBulk", "noreply@shop.test") == 1 })
	_ = stop()
}

func TestRunFatalOnMissingMove(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Caps = mail.Capabilities{"IMAP4REV1"}
	var calls atomic.Int32
	err := Run(bg, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) { calls.Add(1); return fake, nil },
		Backoff: fastBackoff,
	})
	if err == nil || !strings.Contains(err.Error(), "MOVE") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("connected %d times; a refusal must not be retried", calls.Load())
	}
}

func TestRunFatalWithoutInit(t *testing.T) {
	cfg := learnConfig(t, "")
	cfg.State.Path = t.TempDir() + "/q.db"
	st := openStore(t)
	fake := miniMailbox()
	fake.Caps = mailtest.CapsWithMove
	err := Run(bg, cfg, st, RunOptions{Connect: func(context.Context) (mail.Backend, error) { return fake, nil }, Backoff: fastBackoff})
	if err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunFatalWhenLocked(t *testing.T) {
	fake, st, cfg := processBox(t)
	// Another run holds the daemon lock. (A held pass lock is waited for, not
	// refused: see TestRunWaitsForPassLockHeldByAnotherProcess.)
	release, err := AcquireDaemonLock(DaemonLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = Run(bg, cfg, st, RunOptions{Connect: func(context.Context) (mail.Backend, error) { return fake, nil }, Backoff: fastBackoff})
	if err == nil || !strings.Contains(err.Error(), "another quietbox run is already running") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnCancelWhileWaiting(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Wake = make(chan struct{}) // nothing ever wakes it
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) { return fake, nil },
		Backoff: fastBackoff,
	})
	waitFor(t, "the first pass", func() bool { return fake.MoveCount() == 7 })
	if err := stop(); err != nil {
		t.Fatalf("Run = %v, want nil on cancel", err)
	}
}

func TestDefaultBackoffGrowsAndCaps(t *testing.T) {
	if DefaultBackoff(0) != 5*time.Second || DefaultBackoff(1) != 10*time.Second {
		t.Errorf("backoff = %v %v", DefaultBackoff(0), DefaultBackoff(1))
	}
	if DefaultBackoff(50) != 5*time.Minute {
		t.Errorf("cap = %v", DefaultBackoff(50))
	}
}

// lateMail delivers a message right after the first INBOX fetch: after the pass
// has looked, before the loop starts waiting. Nothing ever wakes the wait.
type lateMail struct {
	*mailtest.Fake
	once sync.Once
}

func (l *lateMail) FetchFrom(ctx context.Context, folder string, from uint32, fn func([]mail.Header) error) error {
	err := l.Fake.FetchFrom(ctx, folder, from, fn)
	if folder == "INBOX" {
		l.once.Do(func() { l.Append("INBOX", newMail("late@substack.test", 0, "<late2>", nil)) })
	}
	return err
}

// Review finding: mail that arrives between the end of a pass and the start of
// the IDLE wait must not sit there until the next unrelated delivery.
func TestRunDoesNotMissMailArrivingBeforeTheWait(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Wake = make(chan struct{}) // never fires
	late := &lateMail{Fake: fake}
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) { return late, nil },
		Backoff: fastBackoff,
	})
	waitFor(t, "the mail that arrived just after the first pass", func() bool {
		return fake.CountFrom("SaneNews", "late@substack.test") == 1
	})
	_ = stop()
}

// An expunged top message must not make the loop spin: UIDNEXT stays above the
// highest UID that still exists.
func TestRunDoesNotSpinWhenTheNewestMessageWasDeleted(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Wake = make(chan struct{})
	uid := fake.Append("INBOX", newMail("gone@x.test", 0, "<gone>", nil))
	fake.Remove("INBOX", uid) // deleted by you before quietbox looked
	var fetches atomic.Int32
	counted := &countingFetch{Fake: fake, n: &fetches}
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) { return counted, nil },
		Backoff: fastBackoff,
	})
	waitFor(t, "the first pass", func() bool { return fake.MoveCount() == 7 })
	time.Sleep(100 * time.Millisecond)
	_ = stop()
	if n := fetches.Load(); n > 2 { // one or two passes fetch INBOX once each; a spin would be hundreds
		t.Errorf("%d INBOX fetches: the loop is spinning", n)
	}
}

type countingFetch struct {
	*mailtest.Fake
	n *atomic.Int32
}

// FetchFrom counts INBOX fetches only: a pass also reads Sent and every typed
// folder, which says nothing about spinning.
func (c *countingFetch) FetchFrom(ctx context.Context, folder string, from uint32, fn func([]mail.Header) error) error {
	if folder == "INBOX" {
		c.n.Add(1)
	}
	return c.Fake.FetchFrom(ctx, folder, from, fn)
}

// idleHook calls onIdle each time the run loop enters WaitForChange, before the
// wait starts, so a test can look at the world at that exact moment.
type idleHook struct {
	*mailtest.Fake
	idles  atomic.Int32
	onIdle func()
}

func (h *idleHook) WaitForChange(ctx context.Context, folder string) error {
	if h.onIdle != nil {
		h.onIdle()
	}
	h.idles.Add(1)
	return h.Fake.WaitForChange(ctx, folder)
}

// progressLog collects progress lines from the run goroutine.
type progressLog struct {
	mu    sync.Mutex
	lines []string
}

func (p *progressLog) add(s string) { p.mu.Lock(); p.lines = append(p.lines, s); p.mu.Unlock() }

func (p *progressLog) count(sub string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, l := range p.lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// lockFree reports whether path's flock can be taken right now (and drops it again).
func lockFree(path string) bool {
	release, err := AcquireLock(path)
	if err != nil {
		return false
	}
	release()
	return true
}

func TestRunHoldsNoPassLockWhileIdle(t *testing.T) {
	fake, st, cfg := processBox(t)
	h := &idleHook{Fake: fake} // Wake is nil: the wait lasts until cancel
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) { return h, nil },
		Backoff: fastBackoff,
	})
	waitFor(t, "the run to go idle after its first pass", func() bool { return h.idles.Load() == 1 })
	if fake.MoveCount() != 7 {
		t.Fatalf("moves = %d, want 7 before idling", fake.MoveCount())
	}
	release, err := AcquireLock(LockPath(cfg))
	if err != nil {
		t.Fatalf("pass lock while run idles: %v", err)
	}
	release()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRunHoldsDaemonLockForLife(t *testing.T) {
	fake, st, cfg := processBox(t)
	h := &idleHook{Fake: fake}
	connect := func(context.Context) (mail.Backend, error) { return h, nil }
	stop := startRun(t, cfg, st, RunOptions{Connect: connect, Backoff: fastBackoff})
	waitFor(t, "the first run to go idle", func() bool { return h.idles.Load() == 1 })

	// A short deadline: a regression that lets the second Run in must fail
	// fast, not hang the test until the package timeout.
	ctx2, cancel2 := context.WithTimeout(bg, 3*time.Second)
	defer cancel2()
	err := Run(ctx2, cfg, st, RunOptions{Connect: connect, Backoff: fastBackoff})
	if err == nil || !strings.Contains(err.Error(), "another quietbox run is already running") {
		t.Fatalf("second Run = %v, want the daemon-lock refusal", err)
	}
	if h.idles.Load() != 1 {
		t.Errorf("the refused Run reached the wait")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	// The first run is gone: a new one starts and goes idle.
	stop2 := startRun(t, cfg, st, RunOptions{Connect: connect, Backoff: fastBackoff})
	waitFor(t, "a new run after the first stopped", func() bool { return h.idles.Load() == 2 })
	if err := stop2(); err != nil {
		t.Fatal(err)
	}
}

func TestRunWaitsForPassLockHeldByAnotherProcess(t *testing.T) {
	fake, st, cfg := processBox(t)
	h := &idleHook{Fake: fake}
	// Another fd: flock treats it like another process. The cleanup releases
	// it even if a Fatalf below cuts the test short.
	release := holdPassLock(t, LockPath(cfg))
	var log progressLog
	stop := startRun(t, cfg, st, RunOptions{
		Connect:  func(context.Context) (mail.Backend, error) { return h, nil },
		Backoff:  fastBackoff,
		Progress: log.add,
	})
	waitFor(t, "the waiting notice", func() bool { return log.count("waiting for another quietbox") > 0 })
	if fake.MoveCount() != 0 || h.idles.Load() != 0 {
		t.Fatalf("a pass ran while the pass lock was held: moves=%d idles=%d", fake.MoveCount(), h.idles.Load())
	}
	release()
	waitFor(t, "the pass after the lock was released", func() bool { return fake.MoveCount() == 7 && h.idles.Load() == 1 })
	if n := log.count("waiting for another quietbox"); n != 1 {
		t.Errorf("waiting notice said %d times, want once", n)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCancelWhileWaitingForPassLock(t *testing.T) {
	fake, st, cfg := processBox(t)
	release := holdPassLock(t, LockPath(cfg)) // released by cleanup if a Fatalf cuts the test short
	var connects atomic.Int32
	var log progressLog
	stop := startRun(t, cfg, st, RunOptions{
		Connect:  func(context.Context) (mail.Backend, error) { connects.Add(1); return fake, nil },
		Backoff:  fastBackoff,
		Progress: log.add,
	})
	waitFor(t, "run to wait for the pass lock", func() bool { return log.count("waiting for another quietbox") > 0 })
	start := time.Now()
	if err := stop(); err != nil {
		t.Fatalf("Run = %v, want nil on cancel", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("cancel took %s", d)
	}
	if fake.MoveCount() != 0 {
		t.Errorf("moves = %d, want 0", fake.MoveCount())
	}
	if !lockFree(DaemonLockPath(cfg)) {
		t.Error("daemon lock still held after Run returned")
	}
	release()
	if !lockFree(LockPath(cfg)) {
		t.Error("Run left the pass lock held")
	}
}

func TestRunReleasesPassLockBeforeIdle(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Wake = make(chan struct{}, 1)
	var checked, held atomic.Int32
	h := &idleHook{Fake: fake}
	h.onIdle = func() {
		checked.Add(1)
		if !lockFree(LockPath(cfg)) {
			held.Add(1)
		}
		if lockFree(DaemonLockPath(cfg)) {
			t.Error("daemon lock not held while run idles")
		}
	}
	stop := startRun(t, cfg, st, RunOptions{
		Connect: func(context.Context) (mail.Backend, error) { return h, nil },
		Backoff: fastBackoff,
	})
	waitFor(t, "the first wait", func() bool { return checked.Load() == 1 })
	fake.Append("INBOX", newMail("late@substack.test", 0, "<late>", nil))
	fake.Wake <- struct{}{}
	waitFor(t, "the second wait", func() bool { return checked.Load() == 2 })
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if n := held.Load(); n != 0 {
		t.Errorf("pass lock held at %d of 2 WaitForChange calls", n)
	}
}

func TestRunReleasesLocksOnProcessError(t *testing.T) {
	t.Run("transient error before the backoff", func(t *testing.T) {
		fake, st, cfg := processBox(t)
		f := &flaky{Fake: fake}
		f.failing.Store(true)
		var backoffs, held atomic.Int32
		stop := startRun(t, cfg, st, RunOptions{
			Connect: func(context.Context) (mail.Backend, error) { return f, nil },
			Backoff: func(int) time.Duration {
				if !lockFree(LockPath(cfg)) {
					held.Add(1)
				}
				backoffs.Add(1)
				return time.Millisecond
			},
		})
		waitFor(t, "two failed passes", func() bool { return backoffs.Load() >= 2 })
		if err := stop(); err != nil {
			t.Fatal(err)
		}
		if n := held.Load(); n != 0 {
			t.Errorf("pass lock held during %d backoff(s)", n)
		}
		if !lockFree(LockPath(cfg)) || !lockFree(DaemonLockPath(cfg)) {
			t.Error("a lock is still held after Run returned")
		}
	})
	t.Run("refusal", func(t *testing.T) {
		fake, st, cfg := processBox(t)
		fake.Caps = mail.Capabilities{"IMAP4REV1"}
		err := Run(bg, cfg, st, RunOptions{Connect: func(context.Context) (mail.Backend, error) { return fake, nil }, Backoff: fastBackoff})
		if err == nil || !strings.Contains(err.Error(), "MOVE") {
			t.Fatalf("err = %v", err)
		}
		if !lockFree(LockPath(cfg)) || !lockFree(DaemonLockPath(cfg)) {
			t.Error("a lock is still held after Run refused")
		}
	})
}
