package engine

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
	"github.com/michele/quietbox/internal/store"
)

const blackhole = "SaneBlackHole"

// sweepBox is processBox with a Trash folder and two extra blackholed messages,
// one of them flagged. After the first pass the blackhole folder holds <s1>
// (from the fixture), <n6> (filed by the pass) and <bh1> unflagged, and <bhflag>.
func sweepBox(t *testing.T) (*mailtest.Fake, *store.Store, *config.Config) {
	t.Helper()
	fake, st, cfg := processBox(t)
	fake.Folders = append(fake.Folders, mail.Folder{Name: "Trash", Attrs: []string{`\Trash`}})
	fake.Messages[blackhole] = append(fake.Messages[blackhole],
		mail.Header{From: "block1@bh.test", InternalDate: daysAgo(2), MessageID: "<bh1>"},
		mail.Header{From: "block2@bh.test", InternalDate: daysAgo(3), MessageID: "<bhflag>", Flags: []string{`\Flagged`}},
	)
	return fake, st, cfg
}

func passAt(t *testing.T, fake *mailtest.Fake, st *store.Store, cfg *config.Config, now time.Time, dry bool) *ProcessReport {
	t.Helper()
	rep, err := Process(bg, cfg, fake, st, ProcessOptions{Now: now, DryRun: dry})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func idsIn(fake *mailtest.Fake, folder string) map[string]bool {
	out := map[string]bool{}
	for _, h := range fake.Messages[folder] {
		out[h.MessageID] = true
	}
	return out
}

func sameSet(got map[string]bool, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

func sweptAt(t *testing.T, st *store.Store) (string, bool) {
	t.Helper()
	v, ok, err := st.Meta(bg, store.MetaSweptAt)
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

func purgeRows(t *testing.T, st *store.Store) []store.AuditRow {
	t.Helper()
	rows, err := st.Audit(bg)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditRow
	for _, r := range rows {
		if r.Action == store.ActionMove && r.FromFolder == blackhole && r.ToFolder == "Trash" {
			out = append(out, r)
		}
	}
	return out
}

func TestSweepFirstPassOnlyRecords(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	rep := passAt(t, fake, st, cfg, learnNow, false)
	if len(rep.Purged) != 0 || len(idsIn(fake, "Trash")) != 0 {
		t.Fatalf("nothing is due on the first pass: purged=%+v trash=%v", rep.Purged, idsIn(fake, "Trash"))
	}
	seen, err := st.BlackholeSeen(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"m:<s1>", "m:<n6>", "m:<bh1>", "m:<bhflag>"} {
		if !seen[key].FirstSeen.Equal(learnNow) {
			t.Errorf("seen[%s] = %v, want %v", key, seen[key].FirstSeen, learnNow)
		}
	}
	if v, ok := sweptAt(t, st); !ok || v != strconv.FormatInt(learnNow.Unix(), 10) {
		t.Errorf("swept_at = %q, %v", v, ok)
	}
}

func TestSweepPurgesAfterRetentionAndAuditsFirst(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	passAt(t, fake, st, cfg, learnNow, false)

	fake.OnMove = func(from, to string, uid uint32) {
		if from != blackhole || to != "Trash" {
			return
		}
		for _, r := range purgeRows(t, st) {
			if r.Status == store.AuditPlanned {
				return
			}
		}
		t.Errorf("no planned audit row before purging uid %d", uid)
	}
	later := learnNow.Add(31 * 24 * time.Hour)
	rep := passAt(t, fake, st, cfg, later, false)

	if got := idsIn(fake, "Trash"); !sameSet(got, "<s1>", "<n6>", "<bh1>") {
		t.Errorf("Trash holds %v", got)
	}
	if got := idsIn(fake, blackhole); !sameSet(got, "<bhflag>") {
		t.Errorf("the flagged message should stay alone in %s: %v", blackhole, got)
	}
	if len(rep.Purged) != 3 || rep.PurgeKeptFlagged != 1 || len(rep.Warnings) != 0 {
		t.Fatalf("purged=%+v keptFlagged=%d warnings=%v", rep.Purged, rep.PurgeKeptFlagged, rep.Warnings)
	}
	for _, p := range rep.Purged {
		if p.Status != MoveDone || p.From != blackhole || p.To != "Trash" || p.Sender == "" || p.Rule != "retention" {
			t.Errorf("outcome = %+v", p)
		}
	}
	rows := purgeRows(t, st)
	if len(rows) != 3 {
		t.Fatalf("%d purge rows, want 3", len(rows))
	}
	for _, r := range rows {
		if r.Status != store.AuditDone || r.DestUID == 0 || r.DestUIDValidity == 0 || !strings.Contains(r.Reason, "retention") || !r.At.Equal(later) {
			t.Errorf("audit row = %+v", r)
		}
	}
}

func TestSweepDryRunChangesNothing(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	passAt(t, fake, st, cfg, learnNow, false)
	moves := fake.MoveCount()
	auditBefore, _ := st.Audit(bg)
	seenBefore, _ := st.BlackholeSeen(bg)
	sweptBefore, _ := sweptAt(t, st)

	rep := passAt(t, fake, st, cfg, learnNow.Add(31*24*time.Hour), true)

	if len(rep.Purged) != 3 {
		t.Fatalf("a dry run lists what is due: %+v", rep.Purged)
	}
	for _, p := range rep.Purged {
		if p.Status != MoveWouldMove {
			t.Errorf("status = %q", p.Status)
		}
	}
	auditAfter, _ := st.Audit(bg)
	seenAfter, _ := st.BlackholeSeen(bg)
	sweptAfter, _ := sweptAt(t, st)
	if fake.MoveCount() != moves || len(auditAfter) != len(auditBefore) || len(seenAfter) != len(seenBefore) || sweptAfter != sweptBefore {
		t.Errorf("a dry run wrote something: moves %d->%d audit %d->%d seen %d->%d swept %q->%q",
			moves, fake.MoveCount(), len(auditBefore), len(auditAfter), len(seenBefore), len(seenAfter), sweptBefore, sweptAfter)
	}
}

func TestSweepRunsAtMostOncePerHour(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	cfg.Blackhole.RetentionDur = time.Hour
	passAt(t, fake, st, cfg, learnNow, false)
	fake.Messages[blackhole] = append(fake.Messages[blackhole], mail.Header{From: "late@bh.test", InternalDate: daysAgo(1), MessageID: "<late>"})

	passAt(t, fake, st, cfg, learnNow.Add(30*time.Minute), false)
	if seen, _ := st.BlackholeSeen(bg); !seen["m:<late>"].FirstSeen.IsZero() {
		t.Fatal("the sweep ran again within the hour")
	}
	rep := passAt(t, fake, st, cfg, learnNow.Add(61*time.Minute), false)
	if seen, _ := st.BlackholeSeen(bg); !seen["m:<late>"].FirstSeen.Equal(learnNow.Add(61 * time.Minute)) {
		t.Errorf("late first seen = %v", seen["m:<late>"].FirstSeen)
	}
	if len(rep.Purged) != 3 || idsIn(fake, "Trash")["<late>"] {
		t.Errorf("the old messages are due, the late one is not: %+v", rep.Purged)
	}
}

func TestSweepWithoutTrashWarnsAndMovesNothing(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	fake.Folders = fake.Folders[:len(fake.Folders)-1] // no Trash on the server
	passAt(t, fake, st, cfg, learnNow, false)

	rep := passAt(t, fake, st, cfg, learnNow.Add(31*24*time.Hour), false)
	if len(rep.Purged) != 0 || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "Trash") {
		t.Fatalf("purged=%+v warnings=%v", rep.Purged, rep.Warnings)
	}
	if len(idsIn(fake, blackhole)) != 4 {
		t.Errorf("nothing may leave %s: %v", blackhole, idsIn(fake, blackhole))
	}
}

func TestSweepUsesConfiguredTrashFolder(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	fake.Folders[len(fake.Folders)-1] = mail.Folder{Name: "Deleted"} // no \Trash flag
	cfg.Blackhole.TrashFolder = "Deleted"
	passAt(t, fake, st, cfg, learnNow, false)
	rep := passAt(t, fake, st, cfg, learnNow.Add(31*24*time.Hour), false)
	if len(rep.Purged) != 3 || len(idsIn(fake, "Deleted")) != 3 {
		t.Fatalf("purged=%+v warnings=%v", rep.Purged, rep.Warnings)
	}
}

func TestSweepFailedMoveIsAWarningAndOthersContinue(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	passAt(t, fake, st, cfg, learnNow, false)
	var bad uint32
	for _, h := range fake.Messages[blackhole] {
		if h.MessageID == "<bh1>" {
			bad = h.UID
		}
	}
	fake.MoveFails = map[uint32]error{bad: errors.New("boom")}

	rep := passAt(t, fake, st, cfg, learnNow.Add(31*24*time.Hour), false)
	if got := idsIn(fake, "Trash"); !sameSet(got, "<s1>", "<n6>") {
		t.Errorf("Trash holds %v", got)
	}
	if len(rep.Purged) != 3 || len(rep.Warnings) != 1 {
		t.Fatalf("purged=%+v warnings=%v", rep.Purged, rep.Warnings)
	}
	failed := 0
	for _, r := range purgeRows(t, st) {
		if r.Status == store.AuditFailed && r.MessageKey == "m:<bh1>" {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("want one failed audit row for <bh1>, got %d", failed)
	}
}

func TestSweepOffDoesNothing(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	cfg.Blackhole.RetentionDur = 0
	passAt(t, fake, st, cfg, learnNow, false)
	rep := passAt(t, fake, st, cfg, learnNow.Add(400*24*time.Hour), false)
	if len(rep.Purged) != 0 || len(idsIn(fake, "Trash")) != 0 {
		t.Errorf("purged = %+v", rep.Purged)
	}
	if seen, _ := st.BlackholeSeen(bg); len(seen) != 0 {
		t.Errorf("seen = %v", seen)
	}
	if _, ok := sweptAt(t, st); ok {
		t.Error("swept_at was written")
	}
}

func TestSweepUndoRestoresAndIsNotPurgedAgain(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	passAt(t, fake, st, cfg, learnNow, false)
	purgeAt := learnNow.Add(31 * 24 * time.Hour)
	passAt(t, fake, st, cfg, purgeAt, false)

	ur, err := Undo(bg, cfg, fake, st, UndoOptions{Now: purgeAt.Add(time.Minute), Since: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	restored := 0
	for _, o := range ur.Outcomes {
		if o.Status == UndoRestored {
			restored++
		}
	}
	if restored != 3 || len(idsIn(fake, "Trash")) != 0 {
		t.Fatalf("restored %d, Trash %v", restored, idsIn(fake, "Trash"))
	}

	rep := passAt(t, fake, st, cfg, learnNow.Add(62*24*time.Hour), false)
	if len(rep.Purged) != 0 || rep.PurgeKeptRescued != 3 || len(rep.Trained) != 0 {
		t.Fatalf("purged=%+v keptRescued=%d trained=%+v", rep.Purged, rep.PurgeKeptRescued, rep.Trained)
	}
	if len(idsIn(fake, blackhole)) != 4 || len(idsIn(fake, "Trash")) != 0 {
		t.Errorf("%s holds %v", blackhole, idsIn(fake, blackhole))
	}
}

func TestSweepPurgeInterruptedAfterTheMoveIsSettled(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	passAt(t, fake, st, cfg, learnNow, false)
	var uid uint32
	for _, h := range fake.Messages[blackhole] {
		if h.MessageID == "<bh1>" {
			uid = h.UID
		}
	}
	// The pass wrote the audit row and the server moved the message, then the process died.
	if _, err := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<bh1>", FromFolder: blackhole, ToFolder: "Trash",
		Reason: "retention: interrupted", At: learnNow, Status: store.AuditPlanned}); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Move(bg, blackhole, uid, "Trash"); err != nil {
		t.Fatal(err)
	}

	rep := passAt(t, fake, st, cfg, learnNow.Add(10*time.Minute), false)
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "settled 1") || len(rep.Trained) != 0 {
		t.Fatalf("warnings=%v trained=%+v", rep.Warnings, rep.Trained)
	}
	for _, r := range purgeRows(t, st) {
		if r.Status != store.AuditDone || r.DestUID == 0 {
			t.Errorf("row not settled: %+v", r)
		}
	}
}

// Dragging a swept message from Trash back to INBOX is the way to unblock its
// sender: it reads as a return, pins the sender to INBOX and is not classified.
func TestSweepDragFromTrashToInboxUnblocksTheSender(t *testing.T) {
	fake, st, cfg := sweepBox(t)
	passAt(t, fake, st, cfg, learnNow, false)
	purgeAt := learnNow.Add(31 * 24 * time.Hour)
	passAt(t, fake, st, cfg, purgeAt, false)

	var uid uint32
	for _, h := range fake.Messages["Trash"] {
		if h.MessageID == "<bh1>" {
			uid = h.UID
		}
	}
	if uid == 0 {
		t.Fatal("<bh1> was not swept to Trash")
	}
	if _, err := fake.Move(bg, "Trash", uid, "INBOX"); err != nil {
		t.Fatal(err)
	}

	rep := passAt(t, fake, st, cfg, purgeAt.Add(10*time.Minute), false)
	if len(rep.Trained) != 1 || rep.Trained[0].Sender != "block1@bh.test" || rep.Trained[0].To != "INBOX" {
		t.Fatalf("trained = %+v", rep.Trained)
	}
	if !idsIn(fake, "INBOX")["<bh1>"] || len(rep.Moves) != 0 {
		t.Errorf("the returned message must stay in INBOX: moves=%+v", rep.Moves)
	}
	if s, ok, _ := st.Sender(bg, "block1@bh.test"); !ok || s.RouteFolder != "INBOX" || s.RouteSource != store.SourceTrained {
		t.Errorf("sender = %+v, %v", s, ok)
	}
}
