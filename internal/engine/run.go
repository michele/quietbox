package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// RunOptions controls Run.
type RunOptions struct {
	// Connect dials and logs in; it is called again after every failure.
	Connect func(ctx context.Context) (mail.Backend, error)
	// Backoff is how long to wait before the attempt-th reconnect (0 = first
	// retry). Nil means DefaultBackoff.
	Backoff  func(attempt int) time.Duration
	Progress func(msg string)
}

// DefaultBackoff waits 5s after the first failure, doubling up to 5 minutes.
func DefaultBackoff(attempt int) time.Duration {
	const base, ceiling = 5 * time.Second, 5 * time.Minute
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 6 { // 5s << 6 is already past the ceiling
		return ceiling
	}
	return min(base<<attempt, ceiling)
}

// Run keeps INBOX filed: a Process pass, then wait (IDLE) for the server to
// report a change, then another pass. Connection trouble is retried with
// backoff; a refusal (the server cannot MOVE, nothing learned yet, another
// run already running) ends it with an error. It returns nil when ctx is
// cancelled.
//
// Locks, always in this order: the daemon lock (DaemonLockPath) is taken at
// once, fail-fast, and held until Run returns, so two runs never share a
// mailbox. The pass lock (LockPath) is waited for before each pass and dropped
// as soon as the pass ends, so `once`, `undo` and `senders set` get their turn
// while Run idles. It is never held across the IDLE wait or a backoff.
func Run(ctx context.Context, cfg *config.Config, st *store.Store, o RunOptions) error {
	if o.Backoff == nil {
		o.Backoff = DefaultBackoff
	}
	say := func(format string, a ...any) {
		if o.Progress != nil {
			o.Progress(fmt.Sprintf(format, a...))
		}
	}
	release, err := AcquireDaemonLock(DaemonLockPath(cfg))
	if err != nil {
		return err
	}
	defer release()

	inbox := learnParams(cfg).InboxFolder
	failures := 0
	for ctx.Err() == nil {
		err := runConnected(ctx, cfg, st, o, inbox, say, &failures)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil && isRefusal(err):
			return err
		case err != nil:
			wait := o.Backoff(failures)
			failures++
			say("error: %v; reconnecting in %s", err, wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil
			}
		}
	}
	return nil
}

// runConnected holds one connection: pass, wait, pass, wait ... until it fails.
func runConnected(ctx context.Context, cfg *config.Config, st *store.Store, o RunOptions, inbox string, say func(string, ...any), failures *int) error {
	b, err := o.Connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = b.Close() }()
	for ctx.Err() == nil {
		rep, err := lockedPass(ctx, cfg, b, st, o, say)
		if err != nil {
			return err
		}
		*failures = 0 // a clean pass: the connection is healthy
		say("pass done: %d new, %d moved, %d failed", rep.Fetched, len(rep.Moves)-rep.Failed, rep.Failed)
		for _, t := range rep.Trained {
			say("%s", t)
		}
		if n := rep.TrainedSenders(); n > 0 {
			say("trained %d sender(s)", n)
		}
		for _, n := range rep.Notes {
			say("note: %s", n)
		}
		for _, w := range rep.Warnings {
			say("warning: %s", w)
		}
		// Mail may have arrived after this pass looked but before the wait
		// starts; the server will not announce it again, so check first.
		if more, err := mailWaiting(ctx, b, st, inbox); err != nil {
			return err
		} else if more {
			continue
		}
		if err := b.WaitForChange(ctx, inbox); err != nil {
			return err
		}
	}
	return nil
}

// lockedPass waits for the pass lock, runs one Process pass under it and
// releases it before returning, on every path (error or panic included). A
// cancel while waiting returns ctx.Err(), which Run turns into a clean stop.
func lockedPass(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o RunOptions, say func(string, ...any)) (*ProcessReport, error) {
	release, err := AcquireLockWait(ctx, LockPath(cfg), func() { say("waiting for another quietbox...") })
	if err != nil {
		return nil, err
	}
	defer release()
	return Process(ctx, cfg, b, st, ProcessOptions{LockHeld: true, Progress: o.Progress})
}

// mailWaiting reports whether the folder already holds mail beyond the cursor.
func mailWaiting(ctx context.Context, b mail.Backend, st *store.Store, folder string) (bool, error) {
	cur, ok, err := st.Cursor(ctx, folder)
	if err != nil || !ok {
		return false, err
	}
	fs, err := b.FolderStatus(ctx, folder)
	if err != nil {
		return false, err
	}
	return fs.UIDValidity == cur.UIDValidity && fs.UIDNext > cur.NextUID, nil
}
