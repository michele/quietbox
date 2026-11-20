package imapbackend

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// The go-imap client silently falls back to COPY + \Deleted + EXPUNGE when the
// server has no MOVE. quietbox must never reach that fallback.
func TestMoveGuardRefusesWithoutMoveOrUIDPlus(t *testing.T) {
	cases := []struct {
		name string
		caps imap.CapSet
		want string
	}{
		{"both", imap.CapSet{imap.CapMove: {}, imap.CapUIDPlus: {}}, ""},
		{"no move", imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapUIDPlus: {}}, "MOVE"},
		{"no uidplus", imap.CapSet{imap.CapMove: {}}, "UIDPLUS"},
		{"empty", imap.CapSet{}, "MOVE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := moveGuard(c.caps)
			if c.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to name %s", err, c.want)
			}
		})
	}
}

func TestFlagStrings(t *testing.T) {
	got := flagStrings([]imap.Flag{imap.FlagSeen, imap.FlagFlagged})
	if len(got) != 2 || got[0] != `\Seen` || got[1] != `\Flagged` {
		t.Errorf("flagStrings = %v", got)
	}
	if flagStrings(nil) != nil {
		t.Error("nil flags should stay nil")
	}
}
