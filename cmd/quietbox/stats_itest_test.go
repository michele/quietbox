//go:build integration

package main

import (
	"strings"
	"testing"
)

// Stats are computed from the audit log and the training events: filing,
// a drag back (a correction) and `undo` (not a correction) must show up.
func TestStatsEndToEnd(t *testing.T) {
	e := newM3Env(t)
	e.deliver("INBOX", "writer@news.test", "<st-1@t>")
	e.deliver("INBOX", "writer@news.test", "<st-2@t>")
	e.deliver("INBOX", "store@shop.test", "<st-3@t>")
	e.once()

	out, err := e.run("stats")
	t.Logf("stats after filing:\n%s", out)
	if err != nil || !strings.Contains(out, "Filed away  3 messages") || !strings.Contains(out, "SaneNews") || !strings.Contains(out, "SaneBulk") {
		t.Fatalf("stats after filing: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Corrected by you   0") {
		t.Errorf("nothing was corrected yet:\n%s", out)
	}

	// You disagree with one filing and drag it back.
	e.userMove("SaneNews", "<st-1@t>", "INBOX")
	e.once()
	out, err = e.run("stats")
	t.Logf("stats after the drag back:\n%s", out)
	if err != nil || !strings.Contains(out, "Corrected by you   1") || !strings.Contains(out, "dragged back to INBOX 1") {
		t.Fatalf("stats after the drag back: %v\n%s", err, out)
	}

	// undo reverses the other two moves; it is reported separately and is not a correction.
	if uout, err := e.run("undo", "--since", "1h"); err != nil || !strings.Contains(uout, "2 restored") {
		t.Fatalf("undo: %v\n%s", err, uout)
	}
	out, err = e.run("stats")
	t.Logf("stats after undo:\n%s", out)
	if err != nil || !strings.Contains(out, "Filed away  1 messages") || !strings.Contains(out, "Undone with undo   2") || !strings.Contains(out, "Corrected by you   1") {
		t.Fatalf("stats after undo: %v\n%s", err, out)
	}

	// JSON is available for scripts.
	jout, err := e.run("stats", "--json")
	if err != nil || !strings.Contains(jout, `"filed": 1`) || !strings.Contains(jout, `"undone": 2`) {
		t.Fatalf("stats --json: %v\n%s", err, jout)
	}
}
