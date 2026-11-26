package stats

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestFormatSaved(t *testing.T) {
	for _, c := range []struct {
		s    int
		want string
	}{{0, "0 s"}, {42, "42 s"}, {60, "1 min"}, {2040, "34 min"}, {7500, "2 h 5 min"}, {7200, "2 h"}} {
		if got := FormatSaved(c.s); got != c.want {
			t.Errorf("FormatSaved(%d) = %q, want %q", c.s, got, c.want)
		}
	}
}

func sampleReport() Report {
	return Report{
		Since: now.Add(-30 * 24 * time.Hour), Until: now,
		Filed: 412, SavedSeconds: 2060, SecondsPerMessage: 5,
		Folders: []FolderStat{
			{Folder: "+QuietNews", Count: 201, Share: 0.49, Rules: []RuleCount{{"sender", 140}, {"newsletter", 61}}},
			{Folder: "+QuietBlackHole", Count: 12, Share: 0.03, Purged: 4, Rules: []RuleCount{{"sender", 12}}},
		},
		Corrected: 9, BackToInbox: 6, Refiled: 3, Undone: 2, Retrained: 7, ErrorRate: 9.0 / 412.0,
	}
}

func TestWriteText(t *testing.T) {
	var b bytes.Buffer
	if err := WriteText(&b, sampleReport(), "fastmail"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"Last 30 days (account: fastmail)", "Filed away", "412", "est. time saved", "34 min", "5 s each",
		"+QuietNews", "201", "49%", "sender 140", "newsletter 61", "purged to Trash 4",
		"Corrected by you", "2.2%", "dragged back to INBOX 6", "re-filed elsewhere 3",
		"Undone with undo", "Senders retrained", "counts only mistakes you fixed", "first move per retrained sender",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "@") {
		t.Errorf("output must not contain addresses:\n%s", out)
	}
}

func TestWriteTextEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := WriteText(&b, Report{Since: now.Add(-7 * 24 * time.Hour), Until: now, SecondsPerMessage: 5}, "x"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "Last 7 days") || !strings.Contains(out, "error rate n/a") {
		t.Errorf("empty output:\n%s", out)
	}
	if strings.Contains(out, "counts only mistakes you fixed") || strings.Contains(out, "NaN") {
		t.Errorf("empty output must have no caveat and no NaN:\n%s", out)
	}
}
