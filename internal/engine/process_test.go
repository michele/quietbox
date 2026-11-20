package engine

import (
	"context"
	"errors"
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
