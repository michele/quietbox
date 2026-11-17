package engine

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	src := `
[imap]
host = "127.0.0.1"
port = 1143
tls = "none"
username = "me"

[[folder]]
name = "INBOX"
type = "important"
[[folder]]
name = "SaneLater"
type = "later"
[[folder]]
name = "SaneBulk"
type = "bulk"
[[folder]]
name = "SaneReceipts"
type = "bulk"
[[folder]]
name = "SaneBlackHole"
type = "blackhole"
`
	cfg, err := config.Parse([]byte(src), func(string) string { return "pw" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCheckReport(t *testing.T) {
	fake := &mailtest.Fake{
		Caps: mail.Capabilities{"IDLE", "IMAP4REV1", "MOVE"},
		Folders: []mail.Folder{
			{Name: "INBOX", Messages: 10},
			{Name: "SaneLater", Messages: 8},
			{Name: "SaneBulk", Messages: 9},
			{Name: "SaneBlackHole", Messages: 2},
			{Name: "Sent", Messages: 4},
		},
	}
	r, err := Check(context.Background(), testConfig(t), fake)
	if err != nil {
		t.Fatal(err)
	}
	if r.ObjectID {
		t.Error("ObjectID should be false")
	}
	byName := map[string]FolderStatus{}
	for _, f := range r.Folders {
		byName[f.Name] = f
	}
	if !byName["INBOX"].Exists || byName["INBOX"].Messages != 10 {
		t.Errorf("INBOX = %+v", byName["INBOX"])
	}
	if byName["SaneReceipts"].Exists {
		t.Error("SaneReceipts should be missing")
	}
	if !byName["SaneLater"].DetectTarget {
		t.Error("SaneLater should be a detection target")
	}
	if byName["SaneBulk"].DetectTarget || byName["SaneReceipts"].DetectTarget {
		t.Error("two bulk folders without detect=true: no target")
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "bulk") {
		t.Errorf("warnings = %v", r.Warnings)
	}

	var buf bytes.Buffer
	r.Write(&buf)
	out := buf.String()
	for _, want := range []string{"Message key: fallback", "SaneReceipts", "missing", "init will create it", "Warnings:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCheckObjectID(t *testing.T) {
	fake := &mailtest.Fake{Caps: mail.Capabilities{"OBJECTID"}}
	r, err := Check(context.Background(), testConfig(t), fake)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r.Write(&buf)
	if !r.ObjectID || !strings.Contains(buf.String(), "EMAILID") {
		t.Errorf("expected EMAILID key strategy:\n%s", buf.String())
	}
}

func TestCheckPropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := Check(context.Background(), testConfig(t), &mailtest.Fake{CapsErr: boom}); !errors.Is(err, boom) {
		t.Errorf("caps err = %v", err)
	}
	if _, err := Check(context.Background(), testConfig(t), &mailtest.Fake{FoldersErr: boom}); !errors.Is(err, boom) {
		t.Errorf("folders err = %v", err)
	}
}
