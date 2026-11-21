package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
	"github.com/michele/quietbox/internal/store"
)

// typedFolders are the configured folders of learnConfig other than INBOX and Sent.
var typedFolders = []string{"SaneLater", "SaneNews", "SaneBulk", "SaneBlackHole"}

// uidOf is the UID of the first message from a sender in a folder.
func uidOf(t *testing.T, fake *mailtest.Fake, folder, from string) uint32 {
	t.Helper()
	_, _ = fake.FolderStatus(bg, folder) // assign UIDs
	for _, h := range fake.Messages[folder] {
		if h.From == from {
			return h.UID
		}
	}
	t.Fatalf("no message from %s in %s", from, folder)
	return 0
}

// uidOfID is the UID of the message with a Message-ID in a folder.
func uidOfID(t *testing.T, fake *mailtest.Fake, folder, messageID string) uint32 {
	t.Helper()
	_, _ = fake.FolderStatus(bg, folder) // assign UIDs
	for _, h := range fake.Messages[folder] {
		if h.MessageID == messageID {
			return h.UID
		}
	}
	t.Fatalf("no message %s in %s", messageID, folder)
	return 0
}

// userMove plays the user dragging a message; it returns the new UID.
func userMove(t *testing.T, fake *mailtest.Fake, from string, uid uint32, to string) uint32 {
	t.Helper()
	res, err := fake.Move(bg, from, uid, to)
	if err != nil {
		t.Fatal(err)
	}
	return res.UID
}

func trainingOf(t *testing.T, st *store.Store, sender string) []store.TrainingRow {
	t.Helper()
	rows, err := st.TrainingFor(bg, sender)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func senderOf(t *testing.T, st *store.Store, addr string) (store.SenderRow, bool) {
	t.Helper()
	r, ok, err := st.Sender(bg, addr)
	if err != nil {
		t.Fatal(err)
	}
	return r, ok
}

func trainedSenders(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.Senders(bg, store.SenderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		if r.RouteSource == store.SourceTrained {
			out = append(out, r.Address)
		}
	}
	return out
}

func moveOf(rep *ProcessReport, sender string) (MoveOutcome, bool) {
	for _, m := range rep.Moves {
		if m.Sender == sender {
			return m, true
		}
	}
	return MoveOutcome{}, false
}

func trainedOf(rep *ProcessReport, sender string) []TrainOutcome {
	var out []TrainOutcome
	for _, o := range rep.Trained {
		if o.Sender == sender {
			out = append(out, o)
		}
	}
	return out
}

func hasWarning(rep *ProcessReport, sub string) bool {
	for _, w := range rep.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func setFlags(fake *mailtest.Fake, folder string, uid uint32, flags ...string) {
	for i := range fake.Messages[folder] {
		if fake.Messages[folder][i].UID == uid {
			fake.Messages[folder][i].Flags = flags
		}
	}
}

func newsletterMail(mid string) mail.Header {
	return newMail("newsletter@weekly.test", 0, mid, func(h *mail.Header) {
		h.ListID = "Weekly <weekly.test>"
		sig(h, "list-unsubscribe", "<https://weekly.test/u>")
	})
}

func TestProcessTrainsFromUserFilingIntoTypedFolder(t *testing.T) {
	fake, st, cfg := processBox(t)
	userMove(t, fake, "INBOX", uidOf(t, fake, "INBOX", "stranger@x.test"), "SaneLater")
	rep := runProcess(t, fake, st, cfg, false)
	want := TrainOutcome{Sender: "stranger@x.test", From: "", To: "SaneLater", Source: store.TrainMove, Status: TrainDone}
	if got := trainedOf(rep, "stranger@x.test"); len(got) != 1 || got[0] != want {
		t.Fatalf("trained = %+v, want [%+v]", rep.Trained, want)
	}
	if len(rep.Trained) != 1 {
		t.Errorf("only the user's move trains: %+v", rep.Trained)
	}

	fake.Append("INBOX", newMail("stranger@x.test", 0, "<n5b>", nil))
	rep2 := runProcess(t, fake, st, cfg, false)
	m, ok := moveOf(rep2, "stranger@x.test")
	if !ok || m.To != "SaneLater" || m.Rule != string(classify.RuleSender) || m.Status != MoveDone {
		t.Errorf("new mail from the trained sender: %+v (found %v), want filed to SaneLater by the sender rule", m, ok)
	}
	if len(rep2.Trained) != 0 {
		t.Errorf("our own move trained: %+v", rep2.Trained)
	}
	r, ok := senderOf(t, st, "stranger@x.test")
	if !ok || r.RouteSource != store.SourceTrained || r.RouteFolder != "SaneLater" || r.Mixed {
		t.Errorf("sender row = %+v (found %v)", r, ok)
	}
	if r.Reason != "you moved a message to SaneLater on 2026-06-01" {
		t.Errorf("reason = %q", r.Reason)
	}
	tr := trainingOf(t, st, "stranger@x.test")
	if len(tr) != 1 || tr[0].Source != store.TrainMove || tr[0].Folder != "SaneLater" || tr[0].MessageKey != "m:<n5>" {
		t.Errorf("training = %+v", tr)
	}
}

// filedNewsletter runs one pass (which files newsletter@weekly.test to
// SaneNews) and returns the message's UID there.
func filedNewsletter(t *testing.T, fake *mailtest.Fake, pass func() *ProcessReport) uint32 {
	t.Helper()
	rep := pass()
	if m, ok := moveOf(rep, "newsletter@weekly.test"); !ok || m.To != "SaneNews" || m.Status != MoveDone {
		t.Fatalf("setup: newsletter not filed: %+v", rep.Moves)
	}
	return uidOf(t, fake, "SaneNews", "newsletter@weekly.test")
}

func TestProcessReturnToInboxPinsSender(t *testing.T) {
	fake, st, cfg := processBox(t)
	uid := filedNewsletter(t, fake, func() *ProcessReport { return runProcess(t, fake, st, cfg, false) })
	msgs, _ := st.Messages(bg)
	if r, ok := msgs["m:<n2>"]; !ok || r.Folder != "SaneNews" || !r.InternalDate.Equal(daysAgo(1)) {
		t.Fatalf("our move did not remember the message: %+v (found %v)", r, ok)
	}

	userMove(t, fake, "SaneNews", uid, "INBOX")
	base := fake.MoveCount()
	rep := runProcess(t, fake, st, cfg, false)
	if got := trainedOf(rep, "newsletter@weekly.test"); len(got) != 1 || got[0].To != "INBOX" || got[0].From != "SaneNews" || got[0].Status != TrainDone {
		t.Fatalf("trained = %+v", rep.Trained)
	}
	if r, _ := senderOf(t, st, "newsletter@weekly.test"); r.Reason != "you moved a message from SaneNews to INBOX on 2026-06-01" {
		t.Errorf("reason = %q", r.Reason)
	}
	if fake.MoveCount() != base || countFrom(fake, "INBOX", "newsletter@weekly.test") != 1 {
		t.Error("the returned message was moved again")
	}
	if rep.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the return)", rep.Skipped)
	}
	if msgs, _ := st.Messages(bg); func() bool { _, ok := msgs["m:<n2>"]; return ok }() {
		t.Error("the returned message still has a messages row")
	}

	fake.Append("INBOX", newsletterMail("<n2b>"))
	rep3 := runProcess(t, fake, st, cfg, false)
	if _, moved := moveOf(rep3, "newsletter@weekly.test"); moved || countFrom(fake, "INBOX", "newsletter@weekly.test") != 2 {
		t.Errorf("new mail from a pinned sender left INBOX: %+v", rep3.Moves)
	}
	if rep3.Stayed != 1 {
		t.Errorf("stayed = %d, want 1", rep3.Stayed)
	}
	tr := trainingOf(t, st, "newsletter@weekly.test")
	if len(tr) != 1 || tr[0].Folder != "INBOX" || tr[0].Source != store.TrainMove {
		t.Errorf("training = %+v", tr)
	}
	if r, _ := senderOf(t, st, "newsletter@weekly.test"); r.RouteFolder != "INBOX" || r.RouteSource != store.SourceTrained {
		t.Errorf("sender row = %+v", r)
	}
}

func TestProcessReturnTrainsEvenWhenSeen(t *testing.T) {
	fake, st, cfg := processBox(t)
	if !cfg.Run.SkipSeen {
		t.Fatal("setup: skip_seen should default to true")
	}
	uid := filedNewsletter(t, fake, func() *ProcessReport { return runProcess(t, fake, st, cfg, false) })
	setFlags(fake, "SaneNews", uid, `\Seen`)
	userMove(t, fake, "SaneNews", uid, "INBOX")
	rep := runProcess(t, fake, st, cfg, false)
	if got := trainedOf(rep, "newsletter@weekly.test"); len(got) != 1 || got[0].To != "INBOX" {
		t.Fatalf("a read message dragged back did not train: %+v", rep.Trained)
	}
	if rep.Skipped != 1 || countFrom(fake, "INBOX", "newsletter@weekly.test") != 1 {
		t.Errorf("skipped = %d", rep.Skipped)
	}
}

func TestProcessDoesNotTrainFromOwnMovesOrUndo(t *testing.T) {
	fake, st, cfg := processBox(t)
	rep1 := runProcess(t, fake, st, cfg, false)
	if len(rep1.Moves) != 7 || len(rep1.Trained) != 0 {
		t.Fatalf("first pass: moves=%d trained=%+v", len(rep1.Moves), rep1.Trained)
	}
	rep2 := runProcess(t, fake, st, cfg, false) // sees our moves arrive in the typed folders
	if len(rep2.Trained) != 0 || len(trainedSenders(t, st)) != 0 {
		t.Fatalf("our own moves trained: %+v", rep2.Trained)
	}
	msgs, _ := st.Messages(bg)
	audit, _ := st.Audit(bg)
	var keys []string
	for _, r := range audit {
		if r.Action == store.ActionMove {
			keys = append(keys, r.MessageKey)
			if _, ok := msgs[r.MessageKey]; !ok {
				t.Errorf("our move of %s is not remembered", r.MessageKey)
			}
		}
	}

	if u := runUndo(t, fake, st, cfg, 2*time.Hour, false); countStatus(u, UndoRestored) != 7 {
		t.Fatalf("undo: %+v", u.Outcomes)
	}
	msgs, _ = st.Messages(bg)
	for _, k := range keys {
		if _, ok := msgs[k]; ok {
			t.Errorf("restored message %s still has a messages row", k)
		}
	}
	base := fake.MoveCount()
	rep3 := runProcess(t, fake, st, cfg, false)
	if len(rep3.Trained) != 0 || len(trainedSenders(t, st)) != 0 {
		t.Errorf("undo trained: %+v", rep3.Trained)
	}
	if fake.MoveCount() != base || len(rep3.Moves) != 0 {
		t.Errorf("restored messages were moved again: %+v", rep3.Moves)
	}
	if rep3.Fetched != 7 || rep3.Skipped != 7 {
		t.Errorf("fetched=%d skipped=%d, want 7 and 7", rep3.Fetched, rep3.Skipped)
	}
}

// An undone message has no messages row and its move row is undone, so it is
// not a return; the M3 guard (a key moved once is never moved again) keeps it.
func TestProcessUndoneMessageWithoutMessagesRowIsNeitherTrainedNorMoved(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false)
	runUndo(t, fake, st, cfg, 2*time.Hour, false)
	if msgs, _ := st.Messages(bg); func() bool { _, ok := msgs["m:<n2>"]; return ok }() {
		t.Fatal("setup: undo kept the messages row")
	}
	base := fake.MoveCount()
	rep := runProcess(t, fake, st, cfg, false)
	if len(rep.Trained) != 0 || fake.MoveCount() != base || countFrom(fake, "INBOX", "newsletter@weekly.test") != 1 {
		t.Errorf("trained=%+v moves=%d", rep.Trained, fake.MoveCount()-base)
	}
	if len(trainingOf(t, st, "newsletter@weekly.test")) != 0 {
		t.Error("a training row was written")
	}
}

func TestProcessDragAndBackAgain(t *testing.T) {
	fake, st, cfg := processBox(t)
	uid := filedNewsletter(t, fake, func() *ProcessReport { return runProcess(t, fake, st, cfg, false) })
	inInbox := userMove(t, fake, "SaneNews", uid, "INBOX")
	if rep := runProcess(t, fake, st, cfg, false); len(trainedOf(rep, "newsletter@weekly.test")) != 1 {
		t.Fatalf("return did not train: %+v", rep.Trained)
	}
	userMove(t, fake, "INBOX", inInbox, "SaneNews") // same key, new UID in SaneNews
	rep := runProcess(t, fake, st, cfg, false)
	want := TrainOutcome{Sender: "newsletter@weekly.test", From: "", Route: "INBOX", To: "SaneNews", Source: store.TrainMove, Status: TrainDone}
	if got := trainedOf(rep, "newsletter@weekly.test"); len(got) != 1 || got[0] != want {
		t.Fatalf("trained = %+v, want [%+v]", rep.Trained, want)
	}
	tr := trainingOf(t, st, "newsletter@weekly.test")
	if len(tr) != 2 || tr[0].Folder != "SaneNews" || tr[1].Folder != "INBOX" {
		t.Errorf("training = %+v", tr)
	}
	if r, _ := senderOf(t, st, "newsletter@weekly.test"); r.RouteFolder != "SaneNews" || r.RouteSource != store.SourceTrained ||
		r.Reason != "route was INBOX; you moved a message to SaneNews on 2026-06-01" {
		t.Errorf("sender row = %+v", r)
	}
}

func TestProcessFreshDuplicateDoesNotPin(t *testing.T) {
	t.Run("copy of a message we filed", func(t *testing.T) {
		fake, st, cfg := processBox(t)
		filedNewsletter(t, fake, func() *ProcessReport { return runProcess(t, fake, st, cfg, false) })
		fake.Append("INBOX", newsletterMail("<n2>")) // same Message-ID, delivered later
		rep := runProcess(t, fake, st, cfg, false)
		if len(rep.Trained) != 0 || len(trainingOf(t, st, "newsletter@weekly.test")) != 0 {
			t.Errorf("a fresh duplicate pinned the sender: %+v", rep.Trained)
		}
		if r, ok := senderOf(t, st, "newsletter@weekly.test"); ok && r.RouteSource == store.SourceTrained {
			t.Errorf("sender row = %+v", r)
		}
		if msgs, _ := st.Messages(bg); func() bool { _, ok := msgs["m:<n2>"]; return ok }() == false {
			t.Error("a fresh duplicate is not a return: the messages row must stay")
		}
	})
	t.Run("copy of a message init saw in a typed folder", func(t *testing.T) {
		fake, st, cfg := processBox(t)
		// <e1> from eve@club.test sits in SaneNews since before init (messages row, 20 days old).
		fake.Append("INBOX", newMail("eve@club.test", 0, "<e1>", nil))
		rep := runProcess(t, fake, st, cfg, false)
		if len(trainedOf(rep, "eve@club.test")) != 0 {
			t.Errorf("trained = %+v", rep.Trained)
		}
		if m, ok := moveOf(rep, "eve@club.test"); !ok || m.To != "SaneNews" || m.Status != MoveDone {
			t.Errorf("the duplicate was not classified like new mail: %+v (found %v)", m, ok)
		}
	})
}

func TestProcessM3DatabaseHasNoCursors(t *testing.T) {
	for _, dry := range []bool{false, true} {
		name := "real"
		if dry {
			name = "dry"
		}
		t.Run(name, func(t *testing.T) {
			fake, st, cfg := processBox(t)
			msgs, _ := st.Messages(bg)
			var keys []string
			for k := range msgs {
				keys = append(keys, k)
			}
			if err := st.Update(bg, func(tx *store.Tx) error { return tx.ForgetMessages(keys) }); err != nil {
				t.Fatal(err)
			}
			for _, f := range typedFolders {
				if err := st.DeleteCursor(bg, f); err != nil {
					t.Fatal(err)
				}
			}
			// A move made before the first M4 pass is not learned.
			userMove(t, fake, "INBOX", uidOf(t, fake, "INBOX", "stranger@x.test"), "SaneLater")
			next := map[string]mail.FolderStatus{}
			for _, f := range typedFolders {
				next[f], _ = fake.FolderStatus(bg, f)
			}

			rep := runProcess(t, fake, st, cfg, dry)
			if len(rep.Warnings) != 0 {
				t.Errorf("a first M4 pass is not a fault: warnings = %v", rep.Warnings)
			}
			note := "training from moves starts now for SaneLater, SaneNews, SaneBulk, SaneBlackHole; earlier moves are not learned"
			if !dry && (len(rep.Notes) != 1 || rep.Notes[0] != note) {
				t.Errorf("notes = %q, want [%q]", rep.Notes, note)
			}
			if dry && len(rep.Notes) != 0 {
				t.Errorf("dry run notes = %q", rep.Notes)
			}
			if len(rep.Trained) != 0 || len(trainedSenders(t, st)) != 0 {
				t.Errorf("trained = %+v", rep.Trained)
			}
			if len(rep.Moves) != 7 { // stranger@x.test stays in INBOX anyway: the usual 7
				t.Errorf("moves = %d, want 7", len(rep.Moves))
			}
			for _, f := range typedFolders {
				c, ok, _ := st.Cursor(bg, f)
				if dry {
					if ok {
						t.Errorf("dry run created a cursor for %s", f)
					}
					continue
				}
				if !ok || c.NextUID != next[f].UIDNext || c.UIDValidity != next[f].UIDValidity {
					t.Errorf("cursor %s = %+v (found %v), want UIDNEXT %d", f, c, ok, next[f].UIDNext)
				}
			}
		})
	}
}

func TestProcessTypedFolderUIDValidityChange(t *testing.T) {
	fake, st, cfg := processBox(t)
	userMove(t, fake, "INBOX", uidOf(t, fake, "INBOX", "stranger@x.test"), "SaneLater")
	fake.UIDValidity = map[string]uint32{"SaneLater": 7}
	before, _ := fake.FolderStatus(bg, "SaneLater")
	rep := runProcess(t, fake, st, cfg, false)
	if !hasWarning(rep, "SaneLater was renumbered by the server (UIDVALIDITY changed): nothing was learned from it; run `quietbox init` to relearn") {
		t.Errorf("warnings = %v", rep.Warnings)
	}
	if len(rep.Trained) != 0 || len(trainingOf(t, st, "stranger@x.test")) != 0 {
		t.Errorf("trained across a UIDVALIDITY change: %+v", rep.Trained)
	}
	c, _, _ := st.Cursor(bg, "SaneLater")
	if c.UIDValidity != 7 || c.NextUID != before.UIDNext {
		t.Errorf("cursor = %+v, want validity 7 and UIDNEXT %d", c, before.UIDNext)
	}
}

func TestProcessIgnoresOwnAddressAndEmptyFromArrivals(t *testing.T) {
	fake, st, cfg := processBox(t)
	userMove(t, fake, "INBOX", uidOfID(t, fake, "INBOX", "<n10>"), "SaneLater") // from me@example.test
	fake.Append("SaneLater", mail.Header{MessageID: "<nofrom2>", InternalDate: learnNow})
	fake.Append("SaneNews", newMail("Me@Example.test", 0, "<mixedcase>", nil)) // a backend that did not lower-case
	rep := runProcess(t, fake, st, cfg, false)
	if len(rep.Trained) != 0 || len(trainedSenders(t, st)) != 0 || len(trainingOf(t, st, "me@example.test")) != 0 {
		t.Errorf("trained = %+v", rep.Trained)
	}
	msgs, _ := st.Messages(bg)
	for _, k := range []string{"m:<n10>", "m:<nofrom2>"} {
		if r, ok := msgs[k]; !ok || r.Folder != "SaneLater" {
			t.Errorf("arrival %s not remembered: %+v", k, r)
		}
	}
}

func TestProcessConflictingEventsLastWins(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Append("SaneLater", newMail("flip@x.test", 0, "<f1>", nil))
	fake.Append("SaneBulk", newMail("flip@x.test", 0, "<f2>", nil)) // later in config order
	rep := runProcess(t, fake, st, cfg, false)
	if len(rep.Notes) != 1 || rep.Notes[0] != "moves for flip@x.test disagreed in one pass; the last one won" || len(rep.Warnings) != 0 {
		t.Errorf("notes = %q, warnings = %q", rep.Notes, rep.Warnings)
	}
	if r, _ := senderOf(t, st, "flip@x.test"); r.RouteFolder != "SaneBulk" || r.RouteSource != store.SourceTrained {
		t.Errorf("sender row = %+v", r)
	}
	tr := trainingOf(t, st, "flip@x.test")
	if len(tr) != 2 || tr[0].Folder != "SaneBulk" {
		t.Errorf("training = %+v", tr)
	}
	got := trainedOf(rep, "flip@x.test")
	if len(got) != 2 || got[0].To != "SaneLater" || got[1].Route != "SaneLater" || got[1].To != "SaneBulk" {
		t.Errorf("trained = %+v", got)
	}
}

func TestProcessDryRunTrainsInMemoryOnly(t *testing.T) {
	fake, st, cfg := processBox(t)
	userMove(t, fake, "INBOX", uidOf(t, fake, "INBOX", "stranger@x.test"), "SaneLater")
	fake.Append("INBOX", newMail("stranger@x.test", 0, "<n5b>", nil))
	cursors := map[string]store.Cursor{}
	for _, f := range append([]string{"INBOX", "Sent"}, typedFolders...) {
		cursors[f], _, _ = st.Cursor(bg, f)
	}
	msgsBefore, _ := st.Messages(bg)
	base := fake.MoveCount()

	rep := runProcess(t, fake, st, cfg, true)
	want := TrainOutcome{Sender: "stranger@x.test", To: "SaneLater", Source: store.TrainMove, Status: TrainWould}
	if got := trainedOf(rep, "stranger@x.test"); len(got) != 1 || got[0] != want {
		t.Errorf("trained = %+v, want [%+v]", rep.Trained, want)
	}
	if m, ok := moveOf(rep, "stranger@x.test"); !ok || m.To != "SaneLater" || m.Status != MoveWouldMove {
		t.Errorf("the dry run did not classify with the new route: %+v", m)
	}
	if fake.MoveCount() != base {
		t.Error("dry run moved mail")
	}
	if _, ok := senderOf(t, st, "stranger@x.test"); ok || len(trainingOf(t, st, "stranger@x.test")) != 0 {
		t.Error("dry run wrote training")
	}
	if msgs, _ := st.Messages(bg); len(msgs) != len(msgsBefore) {
		t.Errorf("dry run changed messages: %d -> %d rows", len(msgsBefore), len(msgs))
	}
	for f, c := range cursors {
		if got, _, _ := st.Cursor(bg, f); got != c {
			t.Errorf("dry run moved the %s cursor: %+v -> %+v", f, c, got)
		}
	}
}

func TestProcessTrainsBeforeClassifying(t *testing.T) {
	fake, st, cfg := processBox(t)
	userMove(t, fake, "INBOX", uidOf(t, fake, "INBOX", "stranger@x.test"), "SaneLater") // A
	fake.Append("INBOX", newMail("stranger@x.test", 0, "<n5b>", nil))                   // B
	rep := runProcess(t, fake, st, cfg, false)
	m, ok := moveOf(rep, "stranger@x.test")
	if !ok || m.To != "SaneLater" || m.Rule != string(classify.RuleSender) || m.Status != MoveDone {
		t.Errorf("B = %+v (found %v), want filed to SaneLater in the same pass", m, ok)
	}
	if countFrom(fake, "INBOX", "stranger@x.test") != 0 {
		t.Error("B is still in INBOX")
	}
}

// A pass interrupted after its training was written but before the cursor
// advanced replays the same arrival: it must not add a second training row.
func TestRecordTrainingIsIdempotent(t *testing.T) {
	_, st, _ := processBox(t)
	ev := trainedRoute{Sender: "s@x.test", Folder: "SaneLater", MessageKey: "m:<k>", Reason: "r", At: learnNow}
	for range 2 {
		if err := st.Update(bg, func(tx *store.Tx) error { return recordTraining(tx, []trainedRoute{ev}, store.TrainMove) }); err != nil {
			t.Fatal(err)
		}
	}
	if tr := trainingOf(t, st, "s@x.test"); len(tr) != 1 {
		t.Errorf("training = %+v, want one row", tr)
	}
}

// A move of ours left `planned` by a crash is settled by reconcile before the
// typed folders are read, so its arrival is not a user move. A dry run does not
// reconcile, but must decide the same way.
func TestProcessCrashedOwnMoveDoesNotTrain(t *testing.T) {
	for _, dry := range []bool{true, false} {
		fake, st, cfg := processBox(t)
		fake.Append("SaneBulk", newMail("crash@x.test", 0, "<rec2>", nil)) // the move happened, the outcome was not recorded
		if _, err := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<rec2>", FromFolder: "INBOX", ToFolder: "SaneBulk",
			Reason: "r", At: learnNow, Status: store.AuditPlanned}); err != nil {
			t.Fatal(err)
		}
		rep := runProcess(t, fake, st, cfg, dry)
		if len(trainedOf(rep, "crash@x.test")) != 0 {
			t.Errorf("dry=%v: our crashed move trained: %+v", dry, rep.Trained)
		}
	}
}

// sendersSnapshot is every senders row, to show a pass changed no route.
func sendersSnapshot(t *testing.T, st *store.Store) []store.SenderRow {
	t.Helper()
	rows, err := st.Senders(bg, store.SenderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertNothingTrained(t *testing.T, st *store.Store, rep *ProcessReport, before []store.SenderRow, senders ...string) {
	t.Helper()
	if len(rep.Trained) != 0 {
		t.Errorf("trained = %+v", rep.Trained)
	}
	if after := sendersSnapshot(t, st); !reflect.DeepEqual(before, after) {
		t.Errorf("senders rows changed:\nbefore %+v\nafter  %+v", before, after)
	}
	for _, s := range senders {
		if tr := trainingOf(t, st, s); len(tr) != 0 {
			t.Errorf("training rows for %s: %+v", s, tr)
		}
	}
}

// A MOVE that reported an error (a dropped connection) but that the server
// carried out: the row is `failed`, and the message turns up in the folder
// quietbox chose. That is our move, not yours.
func TestProcessFailedMoveThatHappenedDoesNotTrain(t *testing.T) {
	fake, st, cfg := processBox(t)
	fake.Append("INBOX", newsletterMail("<p2>"))
	uid := uidOfID(t, fake, "INBOX", "<p2>")
	fake.MoveFails = map[uint32]error{uid: errors.New("connection reset")}
	if rep := runProcess(t, fake, st, cfg, false); rep.Failed != 1 {
		t.Fatalf("setup: failed = %d, want 1", rep.Failed)
	}
	fake.MoveFails = nil
	userMove(t, fake, "INBOX", uid, "SaneNews") // what the server really did
	before := sendersSnapshot(t, st)
	rep := runProcess(t, fake, st, cfg, false)
	assertNothingTrained(t, st, rep, before, "newsletter@weekly.test")
}

// A crash mid-move of a message without a Message-ID: reconcile cannot search
// a hash key, so the row becomes `failed`; the message in its destination is
// still ours.
func TestProcessCrashedHashKeyOwnMoveDoesNotTrain(t *testing.T) {
	fake, st, cfg := processBox(t)
	h := newMail("crash@x.test", 0, "", nil)
	key := mail.Key(h)
	if strings.HasPrefix(key, "m:") {
		t.Fatalf("setup: key %q is not a hash key", key)
	}
	fake.Append("SaneBulk", h)
	id, err := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: key, FromFolder: "INBOX", ToFolder: "SaneBulk",
		Reason: "r", At: learnNow, Status: store.AuditPlanned})
	if err != nil {
		t.Fatal(err)
	}
	before := sendersSnapshot(t, st)
	rep := runProcess(t, fake, st, cfg, false)
	if r := auditRow(t, st, id); r.Status != store.AuditFailed {
		t.Errorf("row = %+v, want failed (a hash key cannot be located)", r)
	}
	assertNothingTrained(t, st, rep, before, "crash@x.test")
}

// `undo` killed between its MOVE and recording the outcome: the undo_move row
// stays `planned`. Reconcile settles it (the message is back in INBOX: done,
// the original move undone, the messages row forgotten), and the return does
// not pin the sender. A dry run decides the same way without writing.
func TestProcessCrashedUndoDoesNotTrain(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry", false: "real"}[dry], func(t *testing.T) {
			fake, st, cfg := processBox(t)
			uid := filedNewsletter(t, fake, func() *ProcessReport { return runProcess(t, fake, st, cfg, false) })
			runProcess(t, fake, st, cfg, false) // sees our move arrive in SaneNews
			var orig store.AuditRow
			audit, _ := st.Audit(bg)
			for _, r := range audit {
				if r.Action == store.ActionMove && r.MessageKey == "m:<n2>" {
					orig = r
				}
			}
			undoID, err := st.AddAudit(bg, store.AuditRow{Action: store.ActionUndoMove, MessageKey: "m:<n2>", FromFolder: "SaneNews", ToFolder: "INBOX",
				Reason: "undo", At: learnNow, Status: store.AuditPlanned})
			if err != nil {
				t.Fatal(err)
			}
			back := userMove(t, fake, "SaneNews", uid, "INBOX") // the undo's MOVE happened
			before := sendersSnapshot(t, st)
			base := fake.MoveCount()
			rep := runProcess(t, fake, st, cfg, dry)
			assertNothingTrained(t, st, rep, before, "newsletter@weekly.test")
			if fake.MoveCount() != base || len(rep.Moves) != 0 || countFrom(fake, "INBOX", "newsletter@weekly.test") != 1 {
				t.Errorf("the restored message was moved again: %+v", rep.Moves)
			}
			u, o := auditRow(t, st, undoID), auditRow(t, st, orig.ID)
			if dry {
				if u.Status != store.AuditPlanned || o.Undone {
					t.Errorf("dry run changed the audit log: undo=%+v orig=%+v", u, o)
				}
				return
			}
			if u.Status != store.AuditDone || u.DestUID != back {
				t.Errorf("undo_move row = %+v, want done with dest uid %d", u, back)
			}
			if !o.Undone {
				t.Errorf("original move row not marked undone: %+v", o)
			}
			if msgs, _ := st.Messages(bg); func() bool { _, ok := msgs["m:<n2>"]; return ok }() {
				t.Error("the restored message still has a messages row")
			}
		})
	}
}

// returnedAndPinned checks that sender was pinned to INBOX by one return of
// key from origin: trained route, a move training row, no messages row left,
// the message still in INBOX, and new mail from the sender stays.
func returnedAndPinned(t *testing.T, fake *mailtest.Fake, st *store.Store, cfg func(dry bool) *ProcessReport, rep *ProcessReport, sender, key, origin string, inInbox int) {
	t.Helper()
	got := trainedOf(rep, sender)
	if len(got) != 1 || got[0].To != "INBOX" || got[0].From != origin || got[0].Status != TrainDone {
		t.Fatalf("trained = %+v", rep.Trained)
	}
	if r, ok := senderOf(t, st, sender); !ok || r.RouteSource != store.SourceTrained || r.RouteFolder != "INBOX" {
		t.Errorf("sender row = %+v (found %v)", r, ok)
	}
	if tr := trainingOf(t, st, sender); len(tr) == 0 || tr[0].Folder != "INBOX" || tr[0].Source != store.TrainMove || tr[0].MessageKey != key {
		t.Errorf("training = %+v", tr)
	}
	if msgs, _ := st.Messages(bg); func() bool { _, ok := msgs[key]; return ok }() {
		t.Errorf("%s still has a messages row", key)
	}
	if countFrom(fake, "INBOX", sender) != inInbox {
		t.Errorf("%d messages from %s in INBOX, want %d", countFrom(fake, "INBOX", sender), sender, inInbox)
	}
	if _, moved := moveOf(rep, sender); moved {
		t.Error("the returned message was moved")
	}
	later := newMail(sender, 0, "<later-"+sender+">", func(h *mail.Header) {
		h.ListID = "Later <x.test>"
		sig(h, "list-unsubscribe", "<https://x.test/u>")
	})
	later.InternalDate = learnNow.Add(time.Hour)
	fake.Append("INBOX", later)
	next := cfg(false)
	if _, moved := moveOf(next, sender); moved || countFrom(fake, "INBOX", sender) != inInbox+1 {
		t.Errorf("new mail from a pinned sender left INBOX: %+v", next.Moves)
	}
}

// A message init saw in a typed folder (known only from its messages row,
// never moved by quietbox) that you drag to INBOX pins its sender.
func TestProcessReturnOfMessageInitSaw(t *testing.T) {
	fake, st, cfg := processBox(t)
	msgs, _ := st.Messages(bg)
	if r, ok := msgs["m:<e1>"]; !ok || r.Folder != "SaneNews" || r.InternalDate.IsZero() {
		t.Fatalf("setup: init did not remember <e1>: %+v", r)
	}
	if moved, _ := st.MovedKeys(bg); func() bool { _, ok := moved["m:<e1>"]; return ok }() {
		t.Fatal("setup: <e1> has an audit move")
	}
	runProcess(t, fake, st, cfg, false) // file the fixture's new INBOX mail first
	uid := userMove(t, fake, "SaneNews", uidOfID(t, fake, "SaneNews", "<e1>"), "INBOX")
	if h := fake.Messages["INBOX"]; h[len(h)-1].UID != uid || !h[len(h)-1].InternalDate.Equal(daysAgo(20)) {
		t.Fatal("setup: the fake's move lost INTERNALDATE")
	}
	base := fake.MoveCount()
	rep := runProcess(t, fake, st, cfg, false)
	if fake.MoveCount() != base || rep.Skipped != 1 {
		t.Errorf("moves=%d skipped=%d", fake.MoveCount()-base, rep.Skipped)
	}
	returnedAndPinned(t, fake, st, func(dry bool) *ProcessReport { return runProcess(t, fake, st, cfg, dry) }, rep, "eve@club.test", "m:<e1>", "SaneNews", 1)
}

// A database without messages rows (M3, or init not re-run) still recognises
// the return of a message quietbox filed, through the audit log's move time.
func TestProcessReturnFoundThroughAuditLog(t *testing.T) {
	fake, st, cfg := processBox(t)
	uid := filedNewsletter(t, fake, func() *ProcessReport { return runProcess(t, fake, st, cfg, false) })
	msgs, _ := st.Messages(bg)
	var keys []string
	for k := range msgs {
		keys = append(keys, k)
	}
	if err := st.Update(bg, func(tx *store.Tx) error { return tx.ForgetMessages(keys) }); err != nil {
		t.Fatal(err)
	}
	userMove(t, fake, "SaneNews", uid, "INBOX")
	base := fake.MoveCount()
	rep := runProcess(t, fake, st, cfg, false)
	if fake.MoveCount() != base || rep.Skipped != 1 {
		t.Errorf("moves=%d skipped=%d", fake.MoveCount()-base, rep.Skipped)
	}
	returnedAndPinned(t, fake, st, func(dry bool) *ProcessReport { return runProcess(t, fake, st, cfg, dry) }, rep, "newsletter@weekly.test", "m:<n2>", "SaneNews", 1)
}

// A message without a Message-ID has a hash key; it trains both ways.
func TestProcessHashKeyTrainsBothWays(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false) // file the fixture's new INBOX mail first
	h := mail.Header{From: "hash@x.test", Date: daysAgo(2), InternalDate: daysAgo(2), Subject: "no id"}
	key := mail.Key(h)
	if strings.HasPrefix(key, "m:") {
		t.Fatalf("setup: key %q is not a hash key", key)
	}
	inbox := fake.Append("INBOX", h)
	userMove(t, fake, "INBOX", inbox, "SaneLater")
	rep := runProcess(t, fake, st, cfg, false)
	if got := trainedOf(rep, "hash@x.test"); len(got) != 1 || got[0].To != "SaneLater" {
		t.Fatalf("filing did not train: %+v", rep.Trained)
	}
	if tr := trainingOf(t, st, "hash@x.test"); len(tr) != 1 || tr[0].MessageKey != key {
		t.Errorf("training = %+v", tr)
	}
	if msgs, _ := st.Messages(bg); msgs[key].Folder != "SaneLater" {
		t.Fatalf("hash-keyed arrival not remembered: %+v", msgs[key])
	}
	userMove(t, fake, "SaneLater", uidOf(t, fake, "SaneLater", "hash@x.test"), "INBOX")
	rep = runProcess(t, fake, st, cfg, false)
	if rep.Skipped != 1 {
		t.Errorf("skipped = %d", rep.Skipped)
	}
	returnedAndPinned(t, fake, st, func(dry bool) *ProcessReport { return runProcess(t, fake, st, cfg, dry) }, rep, "hash@x.test", key, "SaneLater", 1)
}

// A pass that fails after spotting a return but before its INBOX cursor
// advanced is replayed: the return must still be recognised, so the
// messages row may only be forgotten together with the cursor.
func TestProcessReturnSurvivesAnInterruptedPass(t *testing.T) {
	fake, st, cfg := processBox(t)
	runProcess(t, fake, st, cfg, false) // file the fixture's new INBOX mail
	fake.Append("SaneLater", newMail("me@example.test", 0, "<me2>", nil))
	runProcess(t, fake, st, cfg, false) // remembers <me2> in SaneLater, trains nothing (own address)
	if msgs, _ := st.Messages(bg); msgs["m:<me2>"].Folder != "SaneLater" {
		t.Fatal("setup: <me2> not remembered")
	}
	userMove(t, fake, "SaneLater", uidOfID(t, fake, "SaneLater", "<me2>"), "INBOX") // returnExplained (own address)
	nl := newsletterMail("<nl3>")
	nl.From = "other@weekly.test"
	fake.Append("INBOX", nl) // will be filed, and the pass dies right then

	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	fake.OnMove = func(from, to string, uid uint32) { cancel() }
	if _, err := Process(ctx, cfg, fake, st, ProcessOptions{Now: learnNow}); err == nil {
		t.Fatal("setup: the pass did not fail")
	}
	fake.OnMove = nil
	if msgs, _ := st.Messages(bg); msgs["m:<me2>"].Folder != "SaneLater" {
		t.Error("the return's messages row was forgotten before the INBOX cursor advanced")
	}
	rep := runProcess(t, fake, st, cfg, false)
	if rep.Fetched != 1 || rep.Skipped != 1 || rep.Stayed != 0 {
		t.Errorf("replay: fetched=%d skipped=%d stayed=%d, want the return skipped, not classified", rep.Fetched, rep.Skipped, rep.Stayed)
	}
	if msgs, _ := st.Messages(bg); func() bool { _, ok := msgs["m:<me2>"]; return ok }() {
		t.Error("the messages row survived the completed pass")
	}
}
