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
	DryRun   bool      // report what would move; write nothing, move nothing, take no lock
	LockHeld bool      // the caller already holds the instance lock (the run loop does)
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

// LockPath is where the instance lock for a configuration lives.
func LockPath(cfg *config.Config) string { return cfg.State.Path + ".lock" }

// Process looks at the mail that arrived since the last pass and files it.
// It is the one code path behind `quietbox once` and `quietbox run`.
//
// Order: lock, check the server can MOVE, reconcile interrupted moves, check
// UIDVALIDITY, learn from new Sent mail, then file new INBOX mail.
func Process(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ProcessOptions) (*ProcessReport, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if st == nil {
		return nil, store.ErrNotInitialised
	}
	if !o.DryRun && !o.LockHeld {
		release, err := AcquireLock(LockPath(cfg))
		if err != nil {
			return nil, err
		}
		defer release()
	}
	caps, err := b.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if err := mail.RequireMove(caps); err != nil {
		return nil, refuse(err)
	}
	if known, err := st.Folders(ctx); err != nil {
		return nil, err
	} else if len(known) == 0 {
		return nil, refuse(errInitFirst)
	}
	rep := &ProcessReport{}
	if !o.DryRun {
		if err := reconcile(ctx, b, st, rep); err != nil {
			return rep, err
		}
	}
	k, targets, err := KnowledgeFromStore(ctx, cfg, st)
	if err != nil {
		return rep, err
	}
	if err := processSent(ctx, cfg, b, st, o, k, rep); err != nil {
		return rep, err
	}
	if err := processInbox(ctx, b, st, cfg, o, k, targets, rep); err != nil {
		return rep, err
	}
	return rep, nil
}

var errInitFirst = errors.New("the database holds nothing learned yet: run `quietbox init` first")

// refusal marks an error as "quietbox will not run in this state" (server cannot
// MOVE, nothing learned yet, bad config), as opposed to a transient failure such
// as a dropped connection. The run loop retries the latter and stops on the former.
type refusal struct{ err error }

func (r refusal) Error() string { return r.err.Error() }
func (r refusal) Unwrap() error { return r.err }

func refuse(err error) error { return refusal{err} }

func isRefusal(err error) bool {
	var r refusal
	return errors.As(err, &r)
}

func progress(o ProcessOptions, format string, a ...any) {
	if o.Progress != nil {
		o.Progress(fmt.Sprintf(format, a...))
	}
}

// reconcile settles move rows left `planned` by an interrupted run: if the
// message is in the destination folder the move happened, otherwise it did not.
// Either way the row stays in the audit log, so the message is not moved again.
func reconcile(ctx context.Context, b mail.Backend, st *store.Store, rep *ProcessReport) error {
	planned, err := st.PlannedMoves(ctx)
	if err != nil {
		return err
	}
	for _, r := range planned {
		status, validity, uid := store.AuditFailed, uint32(0), uint32(0)
		if id, ok := strings.CutPrefix(r.MessageKey, "m:"); ok {
			found, ok, err := b.Locate(ctx, r.ToFolder, 0, 0, id)
			if err != nil {
				return fmt.Errorf("reconcile audit row %d: %w", r.ID, err)
			}
			if ok {
				fs, err := b.FolderStatus(ctx, r.ToFolder)
				if err != nil {
					return fmt.Errorf("reconcile audit row %d: %w", r.ID, err)
				}
				status, validity, uid = store.AuditDone, fs.UIDValidity, found
			}
		}
		if err := st.SetAuditResult(ctx, r.ID, status, validity, uid); err != nil {
			return err
		}
	}
	if len(planned) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("settled %d move(s) left unfinished by an interrupted run (see the audit log)", len(planned)))
	}
	return nil
}

// checkCursor returns a folder's cursor if it is still valid. When the server's
// UIDVALIDITY changed, the stored UIDs mean nothing: it resets the cursor to the
// folder's current end (unless dry-run), warns, and reports ok=false so the
// caller skips the folder.
func checkCursor(ctx context.Context, b mail.Backend, st *store.Store, o ProcessOptions, folder string, rep *ProcessReport) (cur store.Cursor, ok bool, err error) {
	cur, found, err := st.Cursor(ctx, folder)
	if err != nil {
		return cur, false, err
	}
	if !found {
		return cur, false, refuse(fmt.Errorf("no cursor for %s: run `quietbox init` first", folder))
	}
	fs, err := b.FolderStatus(ctx, folder)
	if err != nil {
		return cur, false, fmt.Errorf("status of %s: %w", folder, err)
	}
	if fs.UIDValidity == cur.UIDValidity {
		return cur, true, nil
	}
	rep.Warnings = append(rep.Warnings, fmt.Sprintf(
		"%s was renumbered by the server (UIDVALIDITY changed): nothing was moved from it; run `quietbox init` to relearn", folder))
	if !o.DryRun {
		if err := st.SetCursor(ctx, store.Cursor{Folder: folder, UIDValidity: fs.UIDValidity, NextUID: fs.UIDNext}); err != nil {
			return cur, false, err
		}
	}
	return cur, false, nil
}

// processSent learns from mail you sent since the last pass: new recipients
// count as "you wrote to them" and new Message-IDs make replies recognisable.
// The Knowledge k is updated in memory even in a dry run, so a dry run decides
// exactly as the real run would; only a real run persists it.
func processSent(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ProcessOptions, k classify.Knowledge, rep *ProcessReport) error {
	server, err := b.ListFolders(ctx)
	if err != nil {
		return err
	}
	sentName, err := sentFolder(cfg, server)
	if err != nil {
		return refuse(err)
	}
	cur, ok, err := checkCursor(ctx, b, st, o, sentName, rep)
	if err != nil || !ok {
		return err
	}
	sent := map[string]*store.SentRow{}
	var ids []store.SentMsg
	maxUID := cur.NextUID - 1
	if err := b.FetchFrom(ctx, sentName, cur.NextUID, func(hs []mail.Header) error {
		for _, h := range hs {
			maxUID = max(maxUID, h.UID)
			when := sentWhen(h)
			if h.MessageID != "" {
				ids = append(ids, store.SentMsg{MessageID: h.MessageID, At: when})
				k.SentIDs[h.MessageID] = struct{}{}
			}
			addSentRecipients(sent, k.Me, h, when)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("fetch new mail from %s: %w", sentName, err)
	}
	rows := make([]store.SentRow, 0, len(sent))
	for addr, r := range sent {
		k.SentTo[addr] = true
		rows = append(rows, *r)
	}
	if o.DryRun || maxUID < cur.NextUID {
		return nil
	}
	cur.NextUID = maxUID + 1
	return st.Update(ctx, func(tx *store.Tx) error {
		if err := tx.AddSent(rows, ids); err != nil {
			return err
		}
		return tx.SetCursor(cur)
	})
}

// processInbox classifies and files new INBOX mail. Order of safety: the audit
// row is committed (planned) before every MOVE, and the cursor advances only
// after the whole batch, so an interrupted pass can be repeated: messages it
// already moved are in the audit log and are skipped.
func processInbox(ctx context.Context, b mail.Backend, st *store.Store, cfg *config.Config, o ProcessOptions, k classify.Knowledge, targets classify.Targets, rep *ProcessReport) error {
	inbox := k.Inbox
	cur, ok, err := checkCursor(ctx, b, st, o, inbox, rep)
	if err != nil || !ok {
		return err
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
