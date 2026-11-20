package mail

import (
	"strings"
	"testing"
)

func TestCapabilitiesHas(t *testing.T) {
	c := Capabilities{"IMAP4REV1", "OBJECTID"}
	if !c.Has("objectid") || !c.HasObjectID() {
		t.Error("expected OBJECTID")
	}
	if c.Has("MOVE") {
		t.Error("unexpected MOVE")
	}
}

func TestRequireMove(t *testing.T) {
	cases := []struct {
		name    string
		caps    Capabilities
		wantErr string
	}{
		{"both", Capabilities{"IMAP4REV1", "MOVE", "UIDPLUS"}, ""},
		{"lower case", Capabilities{"move", "uidplus"}, ""},
		{"no move", Capabilities{"IMAP4REV1", "UIDPLUS"}, "MOVE"},
		{"no uidplus", Capabilities{"IMAP4REV1", "MOVE"}, "UIDPLUS"},
		{"neither", nil, "MOVE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := RequireMove(c.caps)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to name %s", err, c.wantErr)
			}
		})
	}
}

func TestHeaderHasFlag(t *testing.T) {
	h := Header{Flags: []string{`\Seen`, `\Flagged`}}
	if !h.HasFlag(`\seen`) || !h.HasFlag(`\Flagged`) || h.HasFlag(`\Answered`) {
		t.Errorf("HasFlag wrong for %v", h.Flags)
	}
}
