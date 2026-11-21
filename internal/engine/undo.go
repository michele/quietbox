package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// UndoOptions controls Undo.
type UndoOptions struct {
	Now    time.Time     // zero means time.Now()
	Since  time.Duration // undo moves recorded within this long before Now
	DryRun bool
}

// Undo outcomes.
const (
	UndoRestored          = "restored"
	UndoWouldRestore      = "would_restore"
	UndoSkippedGone       = "skipped_gone"       // no longer in the folder quietbox put it in
	UndoSkippedUnfindable = "skipped_unfindable" // the server renumbered the folder and the key cannot be searched
	UndoFailed            = "failed"
)

// UndoOutcome is one audited move that undo looked at. From is where the
// message was found (where quietbox had put it), To is where it goes back to.
// The audit log holds no addresses or subjects, so neither does this.
type UndoOutcome struct {
	AuditID  int64
	Key      string
	From, To string
	Reason   string // why quietbox moved it in the first place
	At       time.Time
	Status   string
}

// UndoReport lists the moves undo considered, newest first.
type UndoReport struct {
	Outcomes []UndoOutcome
}

// Undo moves messages that quietbox filed within the window back to where they
// came from. It never deletes anything. A message that is no longer where
// quietbox put it (you moved it yourself) is left alone and reported.
func Undo(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o UndoOptions) (*UndoReport, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if st == nil {
		return nil, store.ErrNotInitialised
	}
	if o.Since <= 0 {
		return nil, fmt.Errorf("undo needs a time window: --since must be positive")
	}
	if !o.DryRun {
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
		return nil, err
	}
	rows, err := st.AuditSince(ctx, o.Now.Add(-o.Since), store.ActionMove)
	if err != nil {
		return nil, err
	}
	rep := &UndoReport{}
	for _, r := range rows {
		if r.Status != store.AuditDone || r.Undone {
			continue
		}
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		out := UndoOutcome{AuditID: r.ID, Key: r.MessageKey, From: r.ToFolder, To: r.FromFolder, Reason: r.Reason, At: r.At}
		if err := undoOne(ctx, b, st, o, r, &out); err != nil {
			rep.Outcomes = append(rep.Outcomes, out)
			return rep, err
		}
		rep.Outcomes = append(rep.Outcomes, out)
	}
	return rep, nil
}

// undoTarget finds, in the audit log, the move an undo_move row reverses: the
// newest earlier done, not-undone move of the same key in the opposite
// direction. ok is false when there is none.
func undoTarget(rows []store.AuditRow, u store.AuditRow) (int, bool) {
	found := -1
	for i, r := range rows {
		if r.ID >= u.ID || r.Action != store.ActionMove || !auditDone(r) || r.Undone ||
			r.MessageKey != u.MessageKey || r.FromFolder != u.ToFolder || r.ToFolder != u.FromFolder {
			continue
		}
		if found < 0 || r.ID > rows[found].ID {
			found = i
		}
	}
	return found, found >= 0
}

// undoOne reverses one move. It returns an error only when the audit log cannot
// be written or the server cannot be asked; everything else is an outcome.
func undoOne(ctx context.Context, b mail.Backend, st *store.Store, o UndoOptions, r store.AuditRow, out *UndoOutcome) error {
	messageID, _ := strings.CutPrefix(r.MessageKey, "m:")
	if !strings.HasPrefix(r.MessageKey, "m:") {
		messageID = "" // hash keys cannot be searched for
	}
	uid, found, err := b.Locate(ctx, r.ToFolder, r.DestUIDValidity, r.DestUID, messageID)
	if err != nil {
		return fmt.Errorf("look for the message of audit row %d in %s: %w", r.ID, r.ToFolder, err)
	}
	if !found {
		out.Status = UndoSkippedGone
		if messageID == "" {
			fs, err := b.FolderStatus(ctx, r.ToFolder)
			if err != nil {
				return err
			}
			if r.DestUID == 0 || fs.UIDValidity != r.DestUIDValidity {
				out.Status = UndoSkippedUnfindable
			}
		}
		return nil
	}
	if o.DryRun {
		out.Status = UndoWouldRestore
		return nil
	}
	id, err := st.AddAudit(ctx, store.AuditRow{
		Action: store.ActionUndoMove, MessageKey: r.MessageKey, FromFolder: r.ToFolder, ToFolder: r.FromFolder,
		Reason: fmt.Sprintf("undo of audit row %d", r.ID), At: o.Now, Status: store.AuditPlanned,
	})
	if err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	res, err := b.Move(ctx, r.ToFolder, uid, r.FromFolder)
	if err != nil {
		out.Status = UndoFailed
		out.Reason = fmt.Sprintf("%s (undo failed: %v)", out.Reason, err)
		if serr := st.SetAuditResult(ctx, id, store.AuditFailed, 0, 0); serr != nil {
			return fmt.Errorf("write audit log: %w", serr)
		}
		return nil
	}
	if err := st.SetAuditResult(ctx, id, store.AuditDone, res.UIDValidity, res.UID); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	if err := st.MarkUndone(ctx, r.ID); err != nil {
		return fmt.Errorf("write audit log: %w", err)
	}
	// Back in INBOX, it is no longer a typed-folder message: its return must
	// not be read as a drag back by the user.
	if err := st.ForgetMessage(ctx, r.MessageKey); err != nil {
		return fmt.Errorf("forget restored message: %w", err)
	}
	out.Status = UndoRestored
	return nil
}
