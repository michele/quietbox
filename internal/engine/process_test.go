package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
	"github.com/michele/quietbox/internal/store"
)

var bg = context.Background()

// processBox is learnedMailbox with a server that can MOVE.
func processBox(t *testing.T) (*mailtest.Fake, *store.Store, *config.Config) {
	t.Helper()
	fake, st, cfg := learnedMailbox(t)
	fake.Caps = mailtest.CapsWithMove
	cfg.State.Path = filepath.Join(t.TempDir(), "q.db") // the lock file lives next to it
	return fake, st, cfg
}

func runProcess(t *testing.T, fake *mailtest.Fake, st *store.Store, cfg *config.Config, dry bool) *ProcessReport {
	t.Helper()
	rep, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow, DryRun: dry})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// countFrom counts the messages from one sender in a folder. Message-IDs in the
// shared fixtures collide across folders, senders do not.
func countFrom(fake *mailtest.Fake, folder, from string) int {
	n := 0
	for _, h := range fake.Messages[folder] {
		if h.From == from {
			n++
		}
	}
	return n
}

func inFolder(fake *mailtest.Fake, folder, messageID string) bool {
	for _, h := range fake.Messages[folder] {
		if h.MessageID == messageID {
			return true
		}
	}
	return false
}

func TestProcessMovesNewMailAndAuditsFirst(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.OnMove = func(from, to string, uid uint32) {
		rows, _ := st.Audit(bg)
		for _, r := range rows {
			if r.Action == store.ActionMove && r.Status == store.AuditPlanned && r.FromFolder == from && r.ToFolder == to {
				return
			}
		}
		t.Errorf("no planned audit row before moving uid %d %s -> %s", uid, from, to)
	}
	rep := runProcess(t, fake, st, cfg, false)

	want := map[string]string{
		"newsletter@weekly.test": "SaneNews", "noreply@shop.test": "SaneBulk",
		"promo@store2.test": "SaneLater", "spam@pushy.test": "SaneBlackHole", "store@big.test": "SaneBulk",
	}
	for from, folder := range want {
		if countFrom(fake, "INBOX", from) != 0 {
			t.Errorf("%s is still in INBOX", from)
		}
		if countFrom(fake, folder, from) == 0 {
			t.Errorf("%s should be in %s", from, folder)
		}
	}
	stay := map[string]int{"stranger@x.test": 1, "anon@elsewhere.test": 1, "old2@x.test": 1, "me@example.test": 2} // me: one older message too
	for from, n := range stay {
		if got := countFrom(fake, "INBOX", from); got != n {
			t.Errorf("%s: %d messages in INBOX, want %d (it should stay)", from, got, n)
		}
	}
	if countFrom(fake, "INBOX", "new1@substack.test") != 0 {
		t.Error("new1@substack.test (two copies) should have left INBOX")
	}
	if len(rep.Moves) != len(want)+2 || rep.Failed != 0 { // +2: new1@substack.test was delivered twice
		t.Fatalf("moves = %+v failed=%d", rep.Moves, rep.Failed)
	}
	rows, _ := st.Audit(bg)
	done := 0
	for _, r := range rows {
		if r.Action != store.ActionMove {
			continue
		}
		done++
		if r.Status != store.AuditDone || r.DestUID == 0 || r.DestUIDValidity == 0 || r.MessageKey == "" || r.Reason == "" {
			t.Errorf("audit row incomplete: %+v", r)
		}
	}
	if done != len(want)+2 {
		t.Errorf("%d move rows, want %d", done, len(want)+2)
	}
}

// Two copies of one message delivered in the same pass are both filed: the
// audit-key guard only knows moves from earlier passes.
func TestProcessDuplicateDeliveryMovesBoth(t *testing.T) {
	fake, st, cfg := processBox(t)
	before := countFrom(fake, "SaneNews", "new1@substack.test")
	runProcess(t, fake, st, cfg, false)
	if got := countFrom(fake, "SaneNews", "new1@substack.test") - before; got != 2 || countFrom(fake, "INBOX", "new1@substack.test") != 0 {
		t.Errorf("filed %d copies, want 2, and none left in INBOX", got)
	}
}

func TestProcessIgnoresMailBeforeCursor(t *testing.T) {
	fake, st, cfg := processBox(t)
	rep := runProcess(t, fake, st, cfg, false)
	if rep.Fetched != 11 { // the 11 messages delivered after init (incl. the duplicate and the old one)
		t.Errorf("fetched %d, want 11", rep.Fetched)
	}
	rows, _ := st.Audit(bg)
	for _, r := range rows {
		if strings.Contains(r.MessageKey, "<a1>") {
			t.Error("mail from before init was touched")
		}
	}
}

func TestProcessSecondPassIsNoop(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	before := len(fake.Moves)
	rep := runProcess(t, fake, st, cfg, false)
	if rep.Fetched != 0 || len(rep.Moves) != 0 || len(fake.Moves) != before {
		t.Errorf("second pass: fetched=%d moves=%d", rep.Fetched, len(rep.Moves))
	}
}

func TestProcessSkipsSeenAndFlagged(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Append("INBOX", newMail("seen@substack.test", 0, "<seen>", func(h *mail.Header) { h.Flags = []string{`\Seen`} }))
	fake.Append("INBOX", newMail("flag@substack.test", 0, "<flag>", func(h *mail.Header) { h.Flags = []string{`\Flagged`} }))
	rep := runProcess(t, fake, st, cfg, false)
	if !inFolder(fake, "INBOX", "<seen>") || !inFolder(fake, "INBOX", "<flag>") {
		t.Error("a seen or flagged message was moved")
	}
	if rep.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", rep.Skipped)
	}
}

func TestProcessSkipRespectsConfig(t *testing.T) {
	fake, st, _ := processBox(t)
	cfg := learnConfig(t, "[run]\nskip_seen = false\nskip_flagged = false\n")
	cfg.State.Path = filepath.Join(t.TempDir(), "q.db")
	fake.Append("INBOX", newMail("seen@substack.test", 0, "<seen>", func(h *mail.Header) { h.Flags = []string{`\Seen`, `\Flagged`} }))
	runProcess(t, fake, st, cfg, false)
	if !inFolder(fake, "SaneNews", "<seen>") {
		t.Error("with both skips off a read, flagged message should be filed")
	}
}

// A message whose key is already in the audit log was moved by us (and then
// dragged back, or restored by undo): it is never moved again.
func TestProcessSkipsKeyAlreadyMoved(t *testing.T) {
	for _, status := range []string{store.AuditPlanned, store.AuditDone, store.AuditFailed, "undone"} {
		t.Run(status, func(t *testing.T) {
			fake, st, cfg := processBox(t)
			st2 := status
			if status == "undone" {
				st2 = store.AuditDone
			}
			id, err := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<n1>", FromFolder: "INBOX", ToFolder: "SaneNews", Reason: "earlier", At: learnNow, Status: st2})
			if err != nil {
				t.Fatal(err)
			}
			if status == "undone" {
				_ = st.MarkUndone(bg, id)
			}
			runProcess(t, fake, st, cfg, false)
			if countFrom(fake, "INBOX", "new1@substack.test") != 2 {
				t.Error("a message with an audited move was moved again")
			}
			if countFrom(fake, "SaneNews", "newsletter@weekly.test") != 1 {
				t.Error("other mail should still be filed")
			}
		})
	}
}

func TestProcessDryRun(t *testing.T) {
	fake, st, cfg := processBox(t)
	cBefore, _, _ := st.Cursor(bg, "INBOX")
	aBefore, _ := st.Audit(bg)
	rep := runProcess(t, fake, st, cfg, true)
	if len(fake.Moves) != 0 {
		t.Fatalf("dry run moved mail: %+v", fake.Moves)
	}
	aAfter, _ := st.Audit(bg)
	if len(aAfter) != len(aBefore) {
		t.Error("dry run wrote audit rows")
	}
	if cAfter, _, _ := st.Cursor(bg, "INBOX"); cAfter != cBefore {
		t.Errorf("dry run advanced the cursor: %+v -> %+v", cBefore, cAfter)
	}
	if len(rep.Moves) != 7 {
		t.Fatalf("would-move = %d, want 7", len(rep.Moves))
	}
	for _, m := range rep.Moves {
		if m.Status != MoveWouldMove {
			t.Errorf("status = %q", m.Status)
		}
	}
	// Running for real afterwards still does the work: nothing was consumed.
	if real := runProcess(t, fake, st, cfg, false); len(real.Moves) != 7 {
		t.Errorf("real run after dry run moved %d, want 7", len(real.Moves))
	}
}

func TestProcessFailedMoveContinues(t *testing.T) {
	fake, st, cfg := processBox(t)
	_, _ = fake.FolderStatus(bg, "INBOX") // assign UIDs
	var n1 uint32
	for _, h := range fake.Messages["INBOX"] {
		if h.MessageID == "<n1>" && n1 == 0 {
			n1 = h.UID
		}
	}
	fake.MoveFails = map[uint32]error{n1: errors.New("server said no")}
	rep := runProcess(t, fake, st, cfg, false)
	if rep.Failed != 1 || len(rep.Moves) != 7 {
		t.Fatalf("failed=%d moves=%d", rep.Failed, len(rep.Moves))
	}
	if countFrom(fake, "SaneNews", "newsletter@weekly.test") != 1 {
		t.Error("the move after the failed one did not happen")
	}
	rows, _ := st.Audit(bg)
	failed := 0
	for _, r := range rows {
		if r.Status == store.AuditFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("%d failed audit rows, want 1", failed)
	}
	// The cursor moved on: a failed move is not retried forever.
	if again := runProcess(t, fake, st, cfg, false); again.Fetched != 0 {
		t.Errorf("second pass fetched %d", again.Fetched)
	}
}

func TestProcessRefusesWithoutMove(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Caps = mail.Capabilities{"IMAP4REV1"}
	fetched := false
	fake.OnFetch = func(string) { fetched = true }
	_, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow})
	if err == nil || !strings.Contains(err.Error(), "MOVE") {
		t.Fatalf("err = %v", err)
	}
	if fetched {
		t.Error("fetched mail before checking capabilities")
	}
	if rows, _ := st.Audit(bg); len(rows) != 0 {
		t.Errorf("audit rows written: %+v", rows)
	}
}

func TestProcessNoCursorErrors(t *testing.T) {
	cfg := learnConfig(t, "")
	cfg.State.Path = filepath.Join(t.TempDir(), "q.db")
	st := openStore(t)
	_ = st.Update(bg, func(tx *store.Tx) error {
		return tx.UpsertFolder(store.FolderRow{Name: "INBOX", Type: "important", Present: true})
	})
	fake := miniMailbox()
	fake.Caps = mailtest.CapsWithMove
	_, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow})
	if err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessUIDValidityChangeMovesNothing(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.UIDValidity = map[string]uint32{"INBOX": 5}
	rep := runProcess(t, fake, st, cfg, false)
	if len(fake.Moves) != 0 || len(rep.Moves) != 0 {
		t.Fatalf("moved mail although INBOX's UIDVALIDITY changed: %+v", rep.Moves)
	}
	if len(rep.Warnings) == 0 || !strings.Contains(rep.Warnings[0], "init") {
		t.Errorf("warnings = %v, want one telling the user to re-run init", rep.Warnings)
	}
	c, _, _ := st.Cursor(bg, "INBOX")
	if c.UIDValidity != 5 {
		t.Errorf("cursor validity = %d, want it reset to 5", c.UIDValidity)
	}
	// After the reset only mail delivered from now on is processed.
	fake.Append("INBOX", newMail("brandnew@substack.test", 0, "<brandnew>", nil))
	again := runProcess(t, fake, st, cfg, false)
	if len(again.Moves) != 1 || countFrom(fake, "SaneNews", "brandnew@substack.test") != 1 {
		t.Errorf("after reset: moves = %+v", again.Moves)
	}
}

func TestProcessSentUIDValidityChangeStillFilesInbox(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.UIDValidity = map[string]uint32{"Sent": 9}
	rep := runProcess(t, fake, st, cfg, false)
	if len(rep.Moves) != 7 || len(rep.Warnings) != 1 {
		t.Errorf("moves=%d warnings=%v", len(rep.Moves), rep.Warnings)
	}
}

func TestProcessSentUpdatesKnowledge(t *testing.T) {
	fake, st, cfg := processBox(t)
	// You just wrote to promo@store2.test: its mail must stay in INBOX now.
	fake.Append("Sent", hdr("me@example.test", 0, "<s9>", "promo@store2.test", "Me@Example.test"))
	runProcess(t, fake, st, cfg, false)
	if countFrom(fake, "INBOX", "promo@store2.test") != 1 {
		t.Error("mail from someone you just wrote to was filed away")
	}
	sent, _ := st.SentTo(bg)
	found := false
	for _, r := range sent {
		if r.Address == "promo@store2.test" && r.Count == 1 {
			found = true
		}
		if r.Address == "me@example.test" {
			t.Error("own address recorded as a recipient")
		}
	}
	if !found {
		t.Errorf("sent_to = %+v", sent)
	}
	if ids, _ := st.SentMessageIDs(bg); len(ids) != 3 { // sent1, sent2 and the new one
		t.Errorf("sent ids = %v", ids)
	}
	// Existing recipients keep their counts: this adds, it does not replace.
	for _, r := range sent {
		if r.Address == "alice@friends.test" && r.Count != 1 {
			t.Errorf("alice count = %d", r.Count)
		}
	}
	// The Sent cursor advanced: nothing is counted twice.
	runProcess(t, fake, st, cfg, false)
	for _, r := range mustSent(t, st) {
		if r.Address == "promo@store2.test" && r.Count != 1 {
			t.Errorf("promo count after second pass = %d", r.Count)
		}
	}
}

func mustSent(t *testing.T, st *store.Store) []store.SentRow {
	t.Helper()
	rows, err := st.SentTo(bg)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestProcessDryRunUsesSentButWritesNothing(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Append("Sent", hdr("me@example.test", 0, "<s9>", "promo@store2.test"))
	sentCursor, _, _ := st.Cursor(bg, "Sent")
	rep := runProcess(t, fake, st, cfg, true)
	for _, m := range rep.Moves {
		if m.Sender == "promo@store2.test" {
			t.Error("dry run ignored the new Sent mail, so it would not match the real run")
		}
	}
	if got, _, _ := st.Cursor(bg, "Sent"); got != sentCursor {
		t.Error("dry run advanced the Sent cursor")
	}
	for _, r := range mustSent(t, st) {
		if r.Address == "promo@store2.test" {
			t.Error("dry run recorded a recipient")
		}
	}
}

func auditRow(t *testing.T, st *store.Store, id int64) store.AuditRow {
	t.Helper()
	rows, _ := st.Audit(bg)
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no audit row %d", id)
	return store.AuditRow{}
}

// Simulates a crash after the audit row was written but before the outcome was
// recorded: the message either made it into the destination or it did not.
func TestReconcilePlannedFound(t *testing.T) {
	fake, st, cfg := processBox(t)
	uid := fake.Append("SaneBulk", mail.Header{From: "x@shop.test", MessageID: "<rec1>"})
	id, _ := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<rec1>", FromFolder: "INBOX", ToFolder: "SaneBulk", Reason: "r", At: learnNow, Status: store.AuditPlanned})
	runProcess(t, fake, st, cfg, false)
	r := auditRow(t, st, id)
	if r.Status != store.AuditDone || r.DestUID != uid || r.DestUIDValidity == 0 {
		t.Errorf("row = %+v, want done with dest uid %d", r, uid)
	}
}

func TestReconcilePlannedMissing(t *testing.T) {
	fake, st, cfg := processBox(t)
	ghost, _ := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<ghost>", FromFolder: "INBOX", ToFolder: "SaneBulk", Reason: "r", At: learnNow, Status: store.AuditPlanned})
	hashed, _ := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "h:abc", FromFolder: "INBOX", ToFolder: "SaneBulk", Reason: "r", At: learnNow, Status: store.AuditPlanned})
	rep := runProcess(t, fake, st, cfg, false)
	if auditRow(t, st, ghost).Status != store.AuditFailed || auditRow(t, st, hashed).Status != store.AuditFailed {
		t.Error("planned rows whose message is not in the destination must become failed")
	}
	if len(rep.Warnings) == 0 {
		t.Error("reconciling interrupted moves should be reported")
	}
}

// A planned undo_move whose message is not back where the undo sent it did
// not happen: the row fails and the original move stays as it was.
func TestReconcilePlannedUndoMissing(t *testing.T) {
	fake, st, cfg := processBox(t)
	orig, _ := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<ghost>", FromFolder: "INBOX", ToFolder: "SaneBulk", Reason: "r", At: learnNow, Status: store.AuditDone})
	undo, _ := st.AddAudit(bg, store.AuditRow{Action: store.ActionUndoMove, MessageKey: "m:<ghost>", FromFolder: "SaneBulk", ToFolder: "INBOX", Reason: "r", At: learnNow, Status: store.AuditPlanned})
	runProcess(t, fake, st, cfg, false)
	if r := auditRow(t, st, undo); r.Status != store.AuditFailed {
		t.Errorf("undo_move row = %+v, want failed", r)
	}
	if auditRow(t, st, orig).Undone {
		t.Error("the original move was marked undone by an undo that did not happen")
	}
}

func TestReconcileSkippedInDryRun(t *testing.T) {
	fake, st, cfg := processBox(t)
	id, _ := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<ghost>", FromFolder: "INBOX", ToFolder: "SaneBulk", Reason: "r", At: learnNow, Status: store.AuditPlanned})
	runProcess(t, fake, st, cfg, true)
	if auditRow(t, st, id).Status != store.AuditPlanned {
		t.Error("dry run changed an audit row")
	}
}

func TestProcessFailsWhenLockedButDryRunDoesNot(t *testing.T) {
	fake, st, cfg := processBox(t)
	release, err := AcquireLock(cfg.State.Path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow}); err == nil || !strings.Contains(err.Error(), "another quietbox") {
		t.Fatalf("err = %v", err)
	}
	if len(fake.Moves) != 0 {
		t.Error("moved mail without holding the lock")
	}
	if _, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow, DryRun: true}); err != nil {
		t.Fatalf("dry run should not need the lock: %v", err)
	}
}

func TestProcessLockHeldByCallerIsNotTakenAgain(t *testing.T) {
	fake, st, cfg := processBox(t)
	release, err := AcquireLock(cfg.State.Path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Process(bg, cfg, fake, st, ProcessOptions{Now: learnNow, LockHeld: true}); err != nil {
		t.Fatalf("LockHeld should skip locking: %v", err)
	}
}

// backlogBox is processBox with two read messages appended after the learn run:
// the first lies inside the backlog (below the boundary), the second after it.
func backlogBox(t *testing.T) (*mailtest.Fake, *store.Store, *config.Config, uint32, uint32) {
	t.Helper()
	fake, st, cfg := processBox(t)
	read := func(h *mail.Header) { h.Flags = []string{`\Seen`} }
	in := fake.Append("INBOX", newMail("bk1@substack.test", 0, "<bk1>", read))
	after := fake.Append("INBOX", newMail("bk2@substack.test", 0, "<bk2>", read))
	c, _, _ := st.Cursor(bg, "INBOX")
	c.BacklogBelow = after // everything below `after` is backlog
	if err := st.SetCursor(bg, c); err != nil {
		t.Fatal(err)
	}
	return fake, st, cfg, in, after
}

func TestProcessBacklogIgnoresSeen(t *testing.T) {
	fake, st, cfg, _, _ := backlogBox(t)
	runProcess(t, fake, st, cfg, false)
	if countFrom(fake, "SaneNews", "bk1@substack.test") != 1 {
		t.Error("a read message inside the backlog should be filed")
	}
	if countFrom(fake, "INBOX", "bk2@substack.test") != 1 {
		t.Error("a read message after the boundary must still be left alone")
	}
}

func TestProcessBacklogStillSkipsFlaggedAndMoved(t *testing.T) {
	fake, st, cfg, _, _ := backlogBox(t)
	fake.Append("INBOX", newMail("flagged@substack.test", 0, "<bkflag>", func(h *mail.Header) { h.Flags = []string{`\Seen`, `\Flagged`} }))
	// Put the flagged message inside the backlog too.
	c, _, _ := st.Cursor(bg, "INBOX")
	c.BacklogBelow += 5
	_ = st.SetCursor(bg, c)
	if _, err := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<bk1>", FromFolder: "INBOX", ToFolder: "SaneNews", Reason: "earlier", At: learnNow, Status: store.AuditDone}); err != nil {
		t.Fatal(err)
	}
	runProcess(t, fake, st, cfg, false)
	if countFrom(fake, "INBOX", "flagged@substack.test") != 1 {
		t.Error("flagged mail was swept")
	}
	if countFrom(fake, "INBOX", "bk1@substack.test") != 1 {
		t.Error("a message with an audited move was swept again")
	}
}

func TestProcessBacklogBoundaryClearedAfterPass(t *testing.T) {
	fake, st, cfg, _, _ := backlogBox(t)
	runProcess(t, fake, st, cfg, false)
	if c, _, _ := st.Cursor(bg, "INBOX"); c.BacklogBelow != 0 {
		t.Errorf("boundary = %d, want it cleared once the cursor passed it", c.BacklogBelow)
	}
}

func TestProcessBacklogDryRunDecidesLikeTheRealRunAndKeepsTheBoundary(t *testing.T) {
	fake, st, cfg, _, after := backlogBox(t)
	rep := runProcess(t, fake, st, cfg, true)
	found := false
	for _, m := range rep.Moves {
		if m.Sender == "bk1@substack.test" {
			found = true
		}
	}
	if !found {
		t.Error("the dry run did not list the read backlog message")
	}
	if c, _, _ := st.Cursor(bg, "INBOX"); c.BacklogBelow != after {
		t.Errorf("dry run changed the boundary: %d", c.BacklogBelow)
	}
}
