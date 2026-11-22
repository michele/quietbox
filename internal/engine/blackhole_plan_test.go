package engine

import (
	"testing"
	"time"

	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

func bh(uid uint32, id string, flags ...string) mail.Header {
	return mail.Header{UID: uid, MessageID: id, Flags: flags}
}

func TestPlanSweep(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	ret := 30 * 24 * time.Hour
	ago := func(d time.Duration) store.SeenRow { return store.SeenRow{FirstSeen: now.Add(-d)} }
	day := 24 * time.Hour

	cases := []struct {
		name        string
		hs          []mail.Header
		seen        map[string]store.SeenRow
		rescued     map[string]bool
		wantDue     []uint32
		wantSeen    map[string]time.Time // key -> first seen after the plan
		wantFlagged int
		wantRescued int
	}{
		{
			name:     "first sighting is recorded as now and never due",
			hs:       []mail.Header{bh(1, "a")},
			wantSeen: map[string]time.Time{"m:a": now},
		},
		{
			name:     "an old message keeps its first-seen time and is not due before retention",
			hs:       []mail.Header{bh(1, "a")},
			seen:     map[string]store.SeenRow{"m:a": ago(29 * day)},
			wantSeen: map[string]time.Time{"m:a": now.Add(-29 * day)},
		},
		{
			name:     "due exactly at retention",
			hs:       []mail.Header{bh(7, "a")},
			seen:     map[string]store.SeenRow{"m:a": ago(ret)},
			wantDue:  []uint32{7},
			wantSeen: map[string]time.Time{"m:a": now.Add(-ret)},
		},
		{
			name:     "due after retention, others kept",
			hs:       []mail.Header{bh(1, "old"), bh(2, "new")},
			seen:     map[string]store.SeenRow{"m:old": ago(40 * day), "m:new": ago(2 * day)},
			wantDue:  []uint32{1},
			wantSeen: map[string]time.Time{"m:old": now.Add(-40 * day), "m:new": now.Add(-2 * day)},
		},
		{
			name:        "flagged mail is never due but is still tracked",
			hs:          []mail.Header{bh(1, "a", `\Flagged`)},
			seen:        map[string]store.SeenRow{"m:a": ago(90 * day)},
			wantSeen:    map[string]time.Time{"m:a": now.Add(-90 * day)},
			wantFlagged: 1,
		},
		{
			name:        "a message restored by undo is never due again",
			hs:          []mail.Header{bh(1, "a")},
			seen:        map[string]store.SeenRow{"m:a": ago(90 * day)},
			rescued:     map[string]bool{"m:a": true},
			wantSeen:    map[string]time.Time{"m:a": now.Add(-90 * day)},
			wantRescued: 1,
		},
		{
			name:     "messages that left the folder are forgotten",
			hs:       []mail.Header{bh(1, "stay")},
			seen:     map[string]store.SeenRow{"m:stay": ago(day), "m:gone": ago(50 * day)},
			wantSeen: map[string]time.Time{"m:stay": now.Add(-day)},
		},
		{
			name:     "two copies of one message share one first-seen time and both are due",
			hs:       []mail.Header{bh(1, "dup"), bh(2, "dup")},
			seen:     map[string]store.SeenRow{"m:dup": ago(31 * day)},
			wantDue:  []uint32{1, 2},
			wantSeen: map[string]time.Time{"m:dup": now.Add(-31 * day)},
		},
		{
			name:     "an empty folder has nothing to do",
			seen:     map[string]store.SeenRow{"m:gone": ago(day)},
			wantSeen: map[string]time.Time{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := planSweep(c.hs, c.seen, c.rescued, now, ret)
			var due []uint32
			for _, d := range p.Due {
				due = append(due, d.UID)
			}
			if len(due) != len(c.wantDue) {
				t.Fatalf("due = %v, want %v", due, c.wantDue)
			}
			for i := range due {
				if due[i] != c.wantDue[i] {
					t.Fatalf("due = %v, want %v", due, c.wantDue)
				}
			}
			if len(p.Seen) != len(c.wantSeen) {
				t.Fatalf("seen has %d keys, want %d: %v", len(p.Seen), len(c.wantSeen), p.Seen)
			}
			for k, want := range c.wantSeen {
				if got, ok := p.Seen[k]; !ok || !got.FirstSeen.Equal(want) || got.Key != k {
					t.Errorf("seen[%s] = %+v, want first seen %v", k, got, want)
				}
			}
			if p.KeptFlagged != c.wantFlagged || p.KeptRescued != c.wantRescued {
				t.Errorf("kept flagged/rescued = %d/%d, want %d/%d", p.KeptFlagged, p.KeptRescued, c.wantFlagged, c.wantRescued)
			}
		})
	}
}
