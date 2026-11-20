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
// instance running) ends it with an error. It returns nil when ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config, st *store.Store, o RunOptions) error {
	if o.Backoff == nil {
		o.Backoff = DefaultBackoff
	}
	say := func(format string, a ...any) {
		if o.Progress != nil {
			o.Progress(fmt.Sprintf(format, a...))
		}
	}
	release, err := AcquireLock(LockPath(cfg))
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
		rep, err := Process(ctx, cfg, b, st, ProcessOptions{LockHeld: true, Progress: o.Progress})
		if err != nil {
			return err
		}
		*failures = 0 // a clean pass: the connection is healthy
		say("pass done: %d new, %d moved, %d failed", rep.Fetched, len(rep.Moves)-rep.Failed, rep.Failed)
		for _, w := range rep.Warnings {
			say("warning: %s", w)
		}
		if err := b.WaitForChange(ctx, inbox); err != nil {
			return err
		}
	}
	return nil
}
