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
	release, err := AcquireLock(LockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = Run(bg, cfg, st, RunOptions{Connect: func(context.Context) (mail.Backend, error) { return fake, nil }, Backoff: fastBackoff})
	if err == nil || !strings.Contains(err.Error(), "another quietbox") {
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
		l.once.Do(func() { l.Fake.Append("INBOX", newMail("late@substack.test", 0, "<late2>", nil)) })
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
	if n := fetches.Load(); n > 4 { // a pass fetches INBOX and Sent; a spin would be hundreds
		t.Errorf("%d fetches: the loop is spinning", n)
	}
}

type countingFetch struct {
	*mailtest.Fake
	n *atomic.Int32
}

func (c *countingFetch) FetchFrom(ctx context.Context, folder string, from uint32, fn func([]mail.Header) error) error {
	c.n.Add(1)
	return c.Fake.FetchFrom(ctx, folder, from, fn)
}
