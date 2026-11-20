package imapbackend

import (
	"context"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"
)

// idleRefresh is how long one WaitForChange idles before returning anyway, so
// the caller re-checks the folder at least this often (a var so tests can shrink it).
var idleRefresh = 20 * time.Minute

// WaitForChange idles on folder until the server reports a change, idleRefresh
// has passed (returns nil: look again), the connection fails, or ctx is done
// (returns ctx.Err()).
func (b *Backend) WaitForChange(ctx context.Context, folder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := b.c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("open %q read-only: %w", folder, err)
	}
	// Selecting reports the current message count as an update: forget it.
	select {
	case <-b.wake:
	default:
	}
	idle, err := b.c.Idle()
	if err != nil {
		return fmt.Errorf("idle on %q: %w", folder, err)
	}
	done := make(chan error, 1)
	go func() { done <- idle.Wait() }()
	timer := time.NewTimer(idleRefresh)
	defer timer.Stop()

	var result error
	select {
	case <-b.wake:
	case <-timer.C:
	case <-ctx.Done():
		result = ctx.Err()
	case err := <-done: // the connection ended on its own
		if err == nil {
			err = fmt.Errorf("idle on %q ended unexpectedly", folder)
		}
		return err
	}
	if err := idle.Close(); err != nil && result == nil {
		result = fmt.Errorf("stop idle: %w", err)
	}
	if err := <-done; err != nil && result == nil {
		result = fmt.Errorf("stop idle: %w", err)
	}
	return result
}
