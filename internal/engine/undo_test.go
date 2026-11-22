package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
	"github.com/michele/quietbox/internal/store"
)

var undoNow = learnNow.Add(time.Hour) // Process wrote its audit rows at learnNow

func runUndo(t *testing.T, fake *mailtest.Fake, st *store.Store, cfg *config.Config, since time.Duration, dry bool) *UndoReport {
	t.Helper()
	rep, err := Undo(bg, cfg, fake, st, UndoOptions{Now: undoNow, Since: since, DryRun: dry})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func countStatus(rep *UndoReport, status string) int {
	n := 0
	for _, o := range rep.Outcomes {
		if o.Status == status {
			n++
		}
	}
	return n
}

func TestUndoRestoresMessage(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	rep := runUndo(t, fake, st, cfg, 2*time.Hour, false)
	if countStatus(rep, UndoRestored) != 7 || len(rep.Outcomes) != 7 {
		t.Fatalf("outcomes = %+v", rep.Outcomes)
	}
	for _, from := range []string{"newsletter@weekly.test", "noreply@shop.test", "promo@store2.test", "spam@pushy.test", "store@big.test"} {
		if countFrom(fake, "INBOX", from) != 1 {
			t.Errorf("%s was not restored to INBOX", from)
		}
	}
	if countFrom(fake, "INBOX", "new1@substack.test") != 2 {
		t.Error("both copies of new1@substack.test should be back")
	}
	rows, _ := st.Audit(bg)
	undone, undoRows := 0, 0
	for _, r := range rows {
		if r.Action == store.ActionMove && r.Undone {
			undone++
		}
		if r.Action == store.ActionUndoMove {
			undoRows++
			if r.Status != store.AuditDone || r.ToFolder != "INBOX" || r.FromFolder == "" {
				t.Errorf("undo row = %+v", r)
			}
		}
	}
	if undone != 7 || undoRows != 7 {
		t.Errorf("undone=%d undo rows=%d, want 7 and 7", undone, undoRows)
	}
}

func TestUndoWritesUndoAuditFirst(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	fake.OnMove = func(from, to string, uid uint32) {
		if to != "INBOX" {
			return
		}
		rows, _ := st.Audit(bg)
		for _, r := range rows {
			if r.Action == store.ActionUndoMove && r.Status == store.AuditPlanned && r.FromFolder == from {
				return
			}
		}
		t.Errorf("no planned undo_move row before moving uid %d back from %s", uid, from)
	}
	runUndo(t, fake, st, cfg, 2*time.Hour, false)
}

func TestUndoSkipsMessageMovedElsewhere(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	// You moved the shop receipt on to SaneLater yourself.
	var uid uint32
	for _, h := range fake.Messages["SaneBulk"] {
		if h.From == "noreply@shop.test" {
			uid = h.UID
		}
	}
	if _, err := fake.Move(bg, "SaneBulk", uid, "SaneLater"); err != nil {
		t.Fatal(err)
	}
	rep := runUndo(t, fake, st, cfg, 2*time.Hour, false)
	if countStatus(rep, UndoSkippedGone) != 1 || countStatus(rep, UndoRestored) != 6 {
		t.Fatalf("outcomes = %+v", rep.Outcomes)
	}
	if countFrom(fake, "SaneLater", "noreply@shop.test") != 1 || countFrom(fake, "INBOX", "noreply@shop.test") != 0 {
		t.Error("a message you filed yourself was touched")
	}
}

func TestUndoFallsBackToMessageIDWhenUIDValidityChanged(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Append("INBOX", newMail("uniq@substack.test", 0, "<uniq-1>", nil))
	runProcess(t, fake, st, cfg, false)
	fake.UIDValidity = map[string]uint32{"SaneNews": 77} // the server renumbered the destination
	rep := runUndo(t, fake, st, cfg, 2*time.Hour, false)
	if countFrom(fake, "INBOX", "uniq@substack.test") != 1 || countFrom(fake, "SaneNews", "uniq@substack.test") != 0 {
		t.Errorf("message was not found by Message-ID: outcomes = %+v", rep.Outcomes)
	}
}

func TestUndoHashKeyChangedValidityIsUnfindable(t *testing.T) {
	fake, st, cfg := processBox(t)
	// No Message-ID: its key is a hash, which cannot be searched for.
	fake.Append("INBOX", mail.Header{From: "nomid@substack.test", Subject: "x", Date: learnNow, InternalDate: learnNow})
	runProcess(t, fake, st, cfg, false)
	fake.UIDValidity = map[string]uint32{"SaneNews": 88}
	rep := runUndo(t, fake, st, cfg, 2*time.Hour, false)
	unfindable := 0
	for _, o := range rep.Outcomes {
		if o.Status == UndoSkippedUnfindable {
			if !strings.HasPrefix(o.Key, "h:") {
				t.Errorf("unfindable outcome for key %q", o.Key)
			}
			unfindable++
		}
	}
	if unfindable != 1 {
		t.Fatalf("unfindable = %d, outcomes = %+v", unfindable, rep.Outcomes)
	}
	if countFrom(fake, "SaneNews", "nomid@substack.test") != 1 {
		t.Error("the unfindable message should be left where it is")
	}
}

func TestUndoRespectsSinceWindow(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	if rep := runUndo(t, fake, st, cfg, 30*time.Minute, false); len(rep.Outcomes) != 0 {
		t.Errorf("moves older than --since were undone: %+v", rep.Outcomes)
	}
}

func TestUndoDryRunChangesNothing(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	moves := len(fake.Moves)
	rowsBefore, _ := st.Audit(bg)
	rep := runUndo(t, fake, st, cfg, 2*time.Hour, true)
	if countStatus(rep, UndoWouldRestore) != 7 {
		t.Fatalf("outcomes = %+v", rep.Outcomes)
	}
	if len(fake.Moves) != moves {
		t.Error("dry run moved mail")
	}
	if rowsAfter, _ := st.Audit(bg); len(rowsAfter) != len(rowsBefore) {
		t.Error("dry run wrote audit rows")
	}
	if n := len(mustMovedUndone(t, st)); n != 0 {
		t.Errorf("dry run marked %d rows undone", n)
	}
}

func mustMovedUndone(t *testing.T, st *store.Store) []store.AuditRow {
	t.Helper()
	rows, _ := st.Audit(bg)
	var out []store.AuditRow
	for _, r := range rows {
		if r.Undone {
			out = append(out, r)
		}
	}
	return out
}

// Review focus 4: what undo restored must not be filed away again.
func TestUndoneMessageIsNotMovedAgainByProcess(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	runUndo(t, fake, st, cfg, 2*time.Hour, false)
	moves := len(fake.Moves)
	rep := runProcess(t, fake, st, cfg, false)
	if len(fake.Moves) != moves || len(rep.Moves) != 0 {
		t.Fatalf("restored mail was moved again: %+v", rep.Moves)
	}
	if rep.Skipped != 7 {
		t.Errorf("skipped = %d, want the 7 restored messages", rep.Skipped)
	}
}

func TestUndoRefusesWithoutMove(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	fake.Caps = mail.Capabilities{"IMAP4REV1"}
	if _, err := Undo(bg, cfg, fake, st, UndoOptions{Now: undoNow, Since: time.Hour}); err == nil || !strings.Contains(err.Error(), "MOVE") {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoFailsWhenLocked(t *testing.T) {
	fake, st, cfg := processBox(t)
	release, err := AcquireLock(LockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Undo(bg, cfg, fake, st, UndoOptions{Now: undoNow, Since: time.Hour, LockTimeout: 300 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "another quietbox") {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoTarget(t *testing.T) {
	mv := func(id int64, key, from, to, status string, undone bool) store.AuditRow {
		return store.AuditRow{ID: id, Action: store.ActionMove, MessageKey: key, FromFolder: from, ToFolder: to, Status: status, Undone: undone}
	}
	undo := store.AuditRow{ID: 10, Action: store.ActionUndoMove, MessageKey: "m:<a>", FromFolder: "SaneNews", ToFolder: "INBOX"}
	tests := []struct {
		name   string
		rows   []store.AuditRow
		wantID int64 // 0: none
	}{
		{"the matching move", []store.AuditRow{mv(1, "m:<a>", "INBOX", "SaneNews", store.AuditDone, false)}, 1},
		{"newest of two", []store.AuditRow{mv(1, "m:<a>", "INBOX", "SaneNews", store.AuditDone, false), mv(3, "m:<a>", "INBOX", "SaneNews", store.AuditDone, false)}, 3},
		{"already undone", []store.AuditRow{mv(1, "m:<a>", "INBOX", "SaneNews", store.AuditDone, true)}, 0},
		{"failed move", []store.AuditRow{mv(1, "m:<a>", "INBOX", "SaneNews", store.AuditFailed, false)}, 0},
		{"other key", []store.AuditRow{mv(1, "m:<b>", "INBOX", "SaneNews", store.AuditDone, false)}, 0},
		{"other folder", []store.AuditRow{mv(1, "m:<a>", "INBOX", "SaneLater", store.AuditDone, false)}, 0},
		{"later than the undo", []store.AuditRow{mv(11, "m:<a>", "INBOX", "SaneNews", store.AuditDone, false)}, 0},
		{"undo_move rows are not targets", []store.AuditRow{{ID: 2, Action: store.ActionUndoMove, MessageKey: "m:<a>", FromFolder: "INBOX", ToFolder: "SaneNews", Status: store.AuditDone}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, ok := undoTarget(tt.rows, undo)
			if tt.wantID == 0 {
				if ok {
					t.Errorf("found row %d, want none", tt.rows[i].ID)
				}
				return
			}
			if !ok || tt.rows[i].ID != tt.wantID {
				t.Errorf("undoTarget = %d, %v; want row %d", i, ok, tt.wantID)
			}
		})
	}
}
