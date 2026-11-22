package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// This file holds the blackhole retention sweep: mail that has sat in the
// blackhole folder longer than the retention period is moved to Trash (never
// deleted). The age counts from when the message was first seen in the folder,
// because a move keeps INTERNALDATE and a message blocked late would otherwise
// be trashed almost at once. The first half is pure; the second half does the
// I/O.

// sweepItem is one message the sweep will move.
type sweepItem struct {
	UID  uint32
	Key  string
	From string // lower-cased sender address, for the report
}

// sweepPlan is what planSweep decides.
type sweepPlan struct {
	Due         []sweepItem              // messages past retention, in folder order
	Seen        map[string]store.SeenRow // the first-seen rows to store: exactly the messages now in the folder
	KeptFlagged int                      // past retention but flagged
	KeptRescued int                      // past retention but restored by undo
}

// planSweep decides which messages of the blackhole folder (hs) are due. seen
// holds the first-seen times from earlier sweeps; a message not in it is seen
// now for the first time and is never due yet. A flagged message, and one whose
// key is in rescued (a purge you reversed with undo), is never due. Messages
// that are no longer in the folder drop out of the returned Seen. It does no I/O.
func planSweep(hs []mail.Header, seen map[string]store.SeenRow, rescued map[string]bool, now time.Time, retention time.Duration) sweepPlan {
	p := sweepPlan{Seen: make(map[string]store.SeenRow, len(hs))}
	for _, h := range hs {
		key := mail.Key(h)
		row, ok := p.Seen[key] // a second copy of the same message shares the row
		if !ok {
			if row, ok = seen[key]; !ok {
				row = store.SeenRow{FirstSeen: now}
			}
			row.Key = key // the map key is what counts
			p.Seen[key] = row
		}
		if now.Sub(row.FirstSeen) < retention {
			continue
		}
		switch {
		case h.HasFlag(`\Flagged`):
			p.KeptFlagged++
		case rescued[key]:
			p.KeptRescued++
		default:
			p.Due = append(p.Due, sweepItem{UID: h.UID, Key: key, From: strings.ToLower(h.From)})
		}
	}
	return p
}

// sweepEvery is the least time between two real sweeps. It bounds how much the
// sweep reads (the whole blackhole folder) and how late a message can be
// noticed; a dry run ignores it.
const sweepEvery = time.Hour

// RuleRetention is the Rule of a MoveOutcome made by the blackhole sweep.
const RuleRetention = "retention"

// blackholeFolder is the configured blackhole folder, if there is one.
func blackholeFolder(cfg *config.Config) (string, bool) {
	for _, f := range cfg.Folders {
		if f.Type == config.TypeBlackhole {
			return f.Name, true
		}
	}
	return "", false
}

// trashFolder is where the sweep moves mail: blackhole.trash_folder, else the
// folder the server flags \Trash. It never guesses a name.
func trashFolder(cfg *config.Config, server []mail.Folder) (string, error) {
	if want := cfg.Blackhole.TrashFolder; want != "" {
		for _, f := range server {
			if f.Name == want {
				return want, nil
			}
		}
		return "", fmt.Errorf("blackhole.trash_folder %q does not exist on the server", want)
	}
	for _, f := range server {
		for _, a := range f.Attrs {
			if strings.EqualFold(a, `\Trash`) {
				return f.Name, nil
			}
		}
	}
	return "", errors.New(`no Trash folder found (the server does not mark one with \Trash): set blackhole.trash_folder in the config`)
}

// rescuedKeys are the messages a sweep moved out of the blackhole folder that
// you then brought back with `undo`. They are never swept again.
func rescuedKeys(rows []store.AuditRow, folder string) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		if r.Action == store.ActionMove && r.FromFolder == folder && r.Undone && r.MessageKey != "" {
			out[r.MessageKey] = true
		}
	}
	return out
}

// sweepBlackhole is the last step of a pass. It records when each message in
// the blackhole folder was first seen and moves the ones past the retention
// period to Trash, one audit row first. A real run sweeps at most once an hour;
// a dry run always looks, and writes nothing. A problem that stops the sweep
// (no Trash, a failed move) is a warning; the rest of the pass is unaffected.
func sweepBlackhole(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ProcessOptions, rep *ProcessReport) error {
	folder, ok := blackholeFolder(cfg)
	if !ok || !cfg.Blackhole.SweepEnabled() {
		return nil
	}
	if !o.DryRun {
		v, ok, err := st.Meta(ctx, store.MetaSweptAt)
		if err != nil {
			return err
		}
		if n, perr := strconv.ParseInt(v, 10, 64); ok && perr == nil && o.Now.Sub(time.Unix(n, 0)) < sweepEvery {
			return nil
		}
	}
	server, err := b.ListFolders(ctx)
	if err != nil {
		return err
	}
	present := false
	for _, f := range server {
		present = present || f.Name == folder
	}
	if !present {
		return nil // `init` creates it; nothing to sweep until then
	}
	var hs []mail.Header
	if err := b.FetchFrom(ctx, folder, 1, func(batch []mail.Header) error {
		hs = append(hs, batch...)
		return nil
	}); err != nil {
		return fmt.Errorf("read %s: %w", folder, err)
	}
	seen, err := st.BlackholeSeen(ctx)
	if err != nil {
		return err
	}
	audit, err := st.Audit(ctx)
	if err != nil {
		return err
	}
	plan := planSweep(hs, seen, rescuedKeys(audit, folder), o.Now, cfg.Blackhole.RetentionDur)
	rep.PurgeKeptFlagged, rep.PurgeKeptRescued = plan.KeptFlagged, plan.KeptRescued

	if len(plan.Due) > 0 {
		trash, err := trashFolder(cfg, server)
		if err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d message(s) in %s are past retention but were not moved: %v", len(plan.Due), folder, err))
		} else if err := purgeDue(ctx, b, st, o, cfg, folder, trash, plan.Due, rep); err != nil {
			return err
		}
	}
	if o.DryRun {
		return nil
	}
	return st.Update(ctx, func(tx *store.Tx) error {
		if err := tx.ReplaceBlackholeSeen(plan.Seen); err != nil {
			return err
		}
		return tx.SetMeta(store.MetaSweptAt, strconv.FormatInt(o.Now.Unix(), 10))
	})
}

// purgeDue moves each due message to Trash. The audit row (an ordinary move, so
// undo, reconcile and own-move recognition all apply) is written before the
// move. A failed move is a warning, not an error: the next sweep tries again,
// except that the failed row keeps the message from being re-filed from INBOX.
func purgeDue(ctx context.Context, b mail.Backend, st *store.Store, o ProcessOptions, cfg *config.Config, folder, trash string, due []sweepItem, rep *ProcessReport) error {
	reason := fmt.Sprintf("in %s for %s or more", folder, cfg.Blackhole.Retention)
	failed := 0
	for _, it := range due {
		if err := ctx.Err(); err != nil {
			return err
		}
		out := MoveOutcome{From: folder, To: trash, Sender: it.From, Rule: RuleRetention, Reason: reason}
		if o.DryRun {
			out.Status = MoveWouldMove
			rep.Purged = append(rep.Purged, out)
			continue
		}
		id, err := st.AddAudit(ctx, store.AuditRow{
			Action: store.ActionMove, MessageKey: it.Key, FromFolder: folder, ToFolder: trash,
			Reason: RuleRetention + ": " + reason, At: o.Now, Status: store.AuditPlanned,
		})
		if err != nil {
			return fmt.Errorf("write audit log: %w", err)
		}
		res, err := b.Move(ctx, folder, it.UID, trash)
		if err != nil {
			out.Status = MoveFailed
			out.Reason = fmt.Sprintf("%s (move failed: %v)", reason, err)
			failed++
			if serr := st.SetAuditResult(ctx, id, store.AuditFailed, 0, 0); serr != nil {
				return fmt.Errorf("write audit log: %w", serr)
			}
			rep.Purged = append(rep.Purged, out)
			continue
		}
		out.Status = MoveDone
		if err := st.SetAuditResult(ctx, id, store.AuditDone, res.UIDValidity, res.UID); err != nil {
			return fmt.Errorf("write audit log: %w", err)
		}
		// Trash is not a typed folder: forget where training last saw it, so a
		// drag from Trash back to INBOX reads as a return and unblocks the sender.
		if err := st.ForgetMessage(ctx, it.Key); err != nil {
			return fmt.Errorf("forget purged message: %w", err)
		}
		rep.Purged = append(rep.Purged, out)
	}
	if failed > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d message(s) could not be moved from %s to %s (see the audit log)", failed, folder, trash))
	}
	return nil
}
