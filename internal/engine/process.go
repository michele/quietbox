package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// ProcessOptions controls Process.
type ProcessOptions struct {
	Now      time.Time // "as of" for audit rows; zero means time.Now()
	DryRun   bool      // report what would move; write nothing, move nothing
	Progress func(msg string)
}

// Move statuses in a ProcessReport.
const (
	MoveDone      = "done"
	MoveFailed    = "failed"
	MoveWouldMove = "would_move"
)

// MoveOutcome is one message Process filed (or would file). It holds the
// sender address and the rule that decided, never a subject or body.
type MoveOutcome struct {
	From, To, Sender, Rule, Reason, Status string
}

// ProcessReport is what one pass did.
type ProcessReport struct {
	Fetched  int // new INBOX messages looked at
	Skipped  int // left alone: seen, flagged, or already moved once
	Stayed   int // classified as staying in INBOX
	Failed   int
	Moves    []MoveOutcome
	Warnings []string
}

// Process looks at the mail that arrived since the last pass and files it.
// It is the one code path behind `quietbox once` and `quietbox run`.
func Process(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ProcessOptions) (*ProcessReport, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if st == nil {
		return nil, store.ErrNotInitialised
	}
	caps, err := b.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if err := mail.RequireMove(caps); err != nil {
		return nil, err
	}
	if known, err := st.Folders(ctx); err != nil {
		return nil, err
	} else if len(known) == 0 {
		return nil, errInitFirst
	}
	rep := &ProcessReport{}
	if err := processInbox(ctx, cfg, b, st, o, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

var errInitFirst = errors.New("the database holds nothing learned yet: run `quietbox init` first")

func progress(o ProcessOptions, format string, a ...any) {
	if o.Progress != nil {
		o.Progress(fmt.Sprintf(format, a...))
	}
}

// processInbox classifies and files new INBOX mail. Order of safety: the audit
// row is committed (planned) before every MOVE, and the cursor advances only
// after the whole batch, so an interrupted pass can be repeated: messages it
// already moved are in the audit log and are skipped.
func processInbox(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ProcessOptions, rep *ProcessReport) error {
	k, targets, err := KnowledgeFromStore(ctx, cfg, st)
	if err != nil {
		return err
	}
	inbox := k.Inbox
	cur, ok, err := st.Cursor(ctx, inbox)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no cursor for %s: run `quietbox init` first", inbox)
	}
	moved, err := st.MovedKeys(ctx) // moves from earlier passes only
	if err != nil {
		return err
	}

	var batch []mail.Header
	if err := b.FetchFrom(ctx, inbox, cur.NextUID, func(hs []mail.Header) error {
		batch = append(batch, hs...)
		return nil
	}); err != nil {
		return fmt.Errorf("fetch new mail from %s: %w", inbox, err)
	}
	rep.Fetched = len(batch)
	progress(o, "%s: %d new message(s)", inbox, len(batch))

	maxUID := cur.NextUID - 1
	for _, h := range batch {
		if err := ctx.Err(); err != nil {
			return err
		}
		if h.UID > maxUID {
			maxUID = h.UID
		}
		key := mail.Key(h)
		_, already := moved[key]
		switch {
		case already,
			cfg.Run.SkipSeen && h.HasFlag(`\Seen`),
			cfg.Run.SkipFlagged && h.HasFlag(`\Flagged`):
			rep.Skipped++
			continue
		}
		d := classify.Classify(h, k, targets)
		if d.Folder == inbox {
			rep.Stayed++
			continue
		}
		out := MoveOutcome{From: inbox, To: d.Folder, Sender: strings.ToLower(h.From), Rule: string(d.Rule), Reason: d.Reason}
		if o.DryRun {
			out.Status = MoveWouldMove
			rep.Moves = append(rep.Moves, out)
			continue
		}
		if err := fileMessage(ctx, b, st, o, h, key, d, &out); err != nil {
			return err
		}
		if out.Status == MoveFailed {
			rep.Failed++
		}
		rep.Moves = append(rep.Moves, out)
	}

	if o.DryRun {
		return nil
	}
	if next := maxUID + 1; next > cur.NextUID {
		cur.NextUID = next
		if err := st.SetCursor(ctx, cur); err != nil {
			return err
		}
	}
	return nil
}

// fileMessage records the intent, moves the message, and records the outcome.
// A failed move is not an error for the pass; failing to write the audit log is.
func fileMessage(ctx context.Context, b mail.Backend, st *store.Store, o ProcessOptions, h mail.Header, key string, d classify.Decision, out *MoveOutcome) error {
	id, err := st.AddAudit(ctx, store.AuditRow{
		Action: store.ActionMove, MessageKey: key, FromFolder: out.From, ToFolder: d.Folder,
		Reason: fmt.Sprintf("%s: %s", d.Rule, d.Reason), At: o.Now, Status: store.AuditPlanned,
	})
	if err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	res, err := b.Move(ctx, out.From, h.UID, d.Folder)
	if err != nil {
		out.Status = MoveFailed
		out.Reason = fmt.Sprintf("%s (move failed: %v)", out.Reason, err)
		if serr := st.SetAuditResult(ctx, id, store.AuditFailed, 0, 0); serr != nil {
			return fmt.Errorf("write audit log: %w", serr)
		}
		return nil
	}
	out.Status = MoveDone
	if err := st.SetAuditResult(ctx, id, store.AuditDone, res.UIDValidity, res.UID); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	return nil
}
