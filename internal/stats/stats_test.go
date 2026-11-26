package stats

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func day(daysAgo int) time.Time { return now.Add(-time.Duration(daysAgo) * 24 * time.Hour) }

func mv(key, to, reason string, daysAgo int) Move {
	return Move{Key: key, From: "INBOX", To: to, Reason: reason, At: day(daysAgo), Done: true}
}

func input(moves []Move, events []Event) Input {
	return Input{
		Moves: moves, Events: events, Now: now, Window: 30 * 24 * time.Hour,
		SecondsPerMessage: 5, Inbox: "INBOX",
		FolderOrder:   []string{"INBOX", "+News", "+Bulk", "+BlackHole"},
		RetentionRule: "retention",
	}
}

func folder(t *testing.T, r Report, name string) FolderStat {
	t.Helper()
	for _, f := range r.Folders {
		if f.Folder == name {
			return f
		}
	}
	t.Fatalf("no folder %q in %+v", name, r.Folders)
	return FolderStat{}
}

func TestComputeFiledAndFolders(t *testing.T) {
	failed := mv("f", "+News", "sender: x", 1)
	failed.Done = false
	old := mv("old", "+News", "sender: x", 40) // outside the 30-day window
	r := Compute(input([]Move{
		mv("a", "+News", "sender: x", 1), mv("b", "+News", "sender: x", 2), mv("c", "+News", "sender: x", 3),
		mv("d", "+Bulk", "bulk: x", 4), failed, old,
	}, nil))
	if r.Filed != 4 || r.SavedSeconds != 20 || r.SecondsPerMessage != 5 {
		t.Fatalf("filed=%d saved=%d spm=%d, want 4/20/5", r.Filed, r.SavedSeconds, r.SecondsPerMessage)
	}
	if len(r.Folders) != 2 || r.Folders[0].Folder != "+News" || r.Folders[1].Folder != "+Bulk" {
		t.Fatalf("folder order = %+v, want config order +News, +Bulk", r.Folders)
	}
	if n := folder(t, r, "+News"); n.Count != 3 || n.Share != 0.75 {
		t.Errorf("+News = %+v, want count 3 share 0.75", n)
	}
	if b := folder(t, r, "+Bulk"); b.Count != 1 || b.Share != 0.25 {
		t.Errorf("+Bulk = %+v, want count 1 share 0.25", b)
	}
	if !r.Since.Equal(now.Add(-30*24*time.Hour)) || !r.Until.Equal(now) {
		t.Errorf("window = %v..%v", r.Since, r.Until)
	}
}

func TestComputeUnknownFolderSortsLast(t *testing.T) {
	r := Compute(input([]Move{mv("a", "Zeta", "sender: x", 1), mv("b", "Alpha", "sender: x", 1), mv("c", "+News", "sender: x", 1)}, nil))
	got := []string{r.Folders[0].Folder, r.Folders[1].Folder, r.Folders[2].Folder}
	if got[0] != "+News" || got[1] != "Alpha" || got[2] != "Zeta" {
		t.Fatalf("order = %v, want +News, Alpha, Zeta", got)
	}
}

func TestComputeRules(t *testing.T) {
	r := Compute(input([]Move{
		mv("a", "+News", "sender: route to +News (learned)", 1),
		mv("b", "+News", "sender: route to +News (learned)", 1),
		mv("c", "+News", "newsletter: newsletter detector (weak): list-id", 1),
		mv("d", "+News", "no separator here", 1),
	}, nil))
	n := folder(t, r, "+News")
	want := []RuleCount{{"sender", 2}, {"newsletter", 1}, {"other", 1}}
	if len(n.Rules) != 3 {
		t.Fatalf("rules = %+v, want %+v", n.Rules, want)
	}
	for i := range want {
		if n.Rules[i] != want[i] {
			t.Errorf("rules[%d] = %+v, want %+v (count desc, then name)", i, n.Rules[i], want[i])
		}
	}
}

func TestComputeCorrections(t *testing.T) {
	moves := []Move{
		mv("back", "+News", "sender: x", 10), mv("elsewhere", "+News", "sender: x", 10),
		mv("early", "+News", "sender: x", 5), mv("twice", "+News", "sender: x", 10),
		mv("undone", "+News", "sender: x", 10), mv("manual", "+News", "sender: x", 10),
		mv("ok1", "+Bulk", "bulk: x", 10), mv("ok2", "+Bulk", "bulk: x", 10),
	}
	moves[4].Undone = true
	events := []Event{
		{At: day(9), Sender: "a@x", Folder: "INBOX", Key: "back", ByMove: true},
		{At: day(9), Sender: "b@x", Folder: "+Bulk", Key: "elsewhere", ByMove: true},
		{At: day(8), Sender: "c@x", Folder: "INBOX", Key: "early", ByMove: true}, // before its move (5 days ago is later than 8 days ago)
		{At: day(9), Sender: "d@x", Folder: "+Bulk", Key: "twice", ByMove: true},
		{At: day(8), Sender: "d@x", Folder: "INBOX", Key: "twice", ByMove: true}, // the later event decides
		{At: day(9), Sender: "e@x", Folder: "INBOX", Key: "undone", ByMove: true},
		{At: day(9), Sender: "f@x", Folder: "+Bulk", Key: "manual", ByMove: false},
		{At: day(9), Sender: "g@x", Folder: "INBOX", Key: "unknown-key", ByMove: true},
	}
	r := Compute(input(moves, events))
	if r.Filed != 8 {
		t.Fatalf("filed = %d, want 8", r.Filed)
	}
	if r.Corrected != 3 || r.BackToInbox != 2 || r.Refiled != 1 {
		t.Fatalf("corrected=%d back=%d refiled=%d, want 3/2/1", r.Corrected, r.BackToInbox, r.Refiled)
	}
	if r.ErrorRate != 3.0/8.0 {
		t.Errorf("error rate = %v, want 0.375", r.ErrorRate)
	}
}

func TestComputeUndoneAndRetrained(t *testing.T) {
	moves := []Move{mv("a", "+News", "sender: x", 3), mv("b", "+News", "sender: x", 3), mv("c", "+News", "sender: x", 3)}
	moves[0].Undone, moves[1].Undone = true, true
	events := []Event{
		{At: day(2), Sender: "a@x", Folder: "+Bulk", ByMove: false},
		{At: day(2), Sender: "a@x", Folder: "INBOX", ByMove: true, Key: "zzz"},
		{At: day(1), Sender: "b@x", Folder: "+Bulk", ByMove: false},
	}
	r := Compute(input(moves, events))
	if r.Undone != 2 {
		t.Errorf("undone = %d, want 2", r.Undone)
	}
	if r.Retrained != 2 {
		t.Errorf("retrained = %d, want 2 distinct senders", r.Retrained)
	}
	if r.Filed != 3 {
		t.Errorf("filed = %d: undone moves were still filed", r.Filed)
	}
}

func TestComputePurges(t *testing.T) {
	purge := Move{Key: "p", From: "+BlackHole", To: "Trash", Reason: "retention: in +BlackHole for 30d or more", At: day(2), Done: true}
	r := Compute(input([]Move{mv("a", "+News", "sender: x", 1), purge}, []Event{
		{At: day(1), Sender: "a@x", Folder: "INBOX", Key: "p", ByMove: true}, // a purge is never a correction
	}))
	if r.Filed != 1 {
		t.Fatalf("filed = %d, want 1 (the purge is not filed)", r.Filed)
	}
	if r.Corrected != 0 {
		t.Errorf("corrected = %d, want 0", r.Corrected)
	}
	b := folder(t, r, "+BlackHole")
	if b.Count != 0 || b.Purged != 1 {
		t.Errorf("+BlackHole = %+v, want count 0 purged 1", b)
	}
}

func TestComputeEmpty(t *testing.T) {
	r := Compute(input(nil, nil))
	if r.Filed != 0 || r.ErrorRate != 0 || len(r.Folders) != 0 || r.SavedSeconds != 0 {
		t.Fatalf("empty report = %+v", r)
	}
}
