package engine

import (
	"strings"
	"testing"
)

func TestPurgeLines(t *testing.T) {
	out := func(sender, status string) MoveOutcome {
		return MoveOutcome{From: "SaneBlackHole", To: "Trash", Sender: sender, Rule: RuleRetention, Status: status}
	}
	cases := []struct {
		name   string
		rep    ProcessReport
		dryRun bool
		want   []string
	}{
		{name: "nothing purged says nothing, even with kept mail", rep: ProcessReport{PurgeKeptFlagged: 2}, want: nil},
		{
			name: "a purge lists each message and a summary with what was kept",
			rep: ProcessReport{
				Purged:           []MoveOutcome{out("a@x.test", MoveDone), out("b@x.test", MoveDone), out("c@x.test", MoveFailed)},
				PurgeKeptFlagged: 1, PurgeKeptRescued: 2,
			},
			want: []string{
				"moved a@x.test: SaneBlackHole -> Trash",
				"moved b@x.test: SaneBlackHole -> Trash",
				"could not move c@x.test: SaneBlackHole -> Trash",
				"moved 2 message(s) from SaneBlackHole to Trash (kept: 1 flagged, 2 restored by undo)",
			},
		},
		{
			name:   "a dry run says would move",
			rep:    ProcessReport{Purged: []MoveOutcome{out("a@x.test", MoveWouldMove)}},
			dryRun: true,
			want:   []string{"would move a@x.test: SaneBlackHole -> Trash", "would move 1 message(s) from SaneBlackHole to Trash"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.rep.PurgeLines(c.dryRun)
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}
