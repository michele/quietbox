//go:build integration

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// deliverAt appends a message that arrived at a given time, with extra header
// lines (e.g. List-Id), the way an older message sits in a real mailbox.
func (e *m3Env) deliverAt(folder, from, msgID string, when time.Time, extra []string, flags ...imap.Flag) {
	e.t.Helper()
	c, done := rawLogin(e.t, e.user)
	defer done()
	msg := fmt.Sprintf("From: <%s>\r\nTo: <%s>\r\nSubject: fresh test\r\nDate: %s\r\nMessage-ID: %s\r\n%s\r\nbody\r\n",
		from, e.user, when.UTC().Format(time.RFC1123Z), msgID, strings.Join(append(extra, ""), "\r\n"))
	cmd := c.Append(folder, int64(len(msg)), &imap.AppendOptions{Flags: flags, Time: when})
	if _, err := cmd.Write([]byte(msg)); err != nil {
		e.t.Fatal(err)
	}
	if err := cmd.Close(); err != nil {
		e.t.Fatal(err)
	}
	if _, err := cmd.Wait(); err != nil {
		e.t.Fatal(err)
	}
}

var newsletterHeaders = []string{"List-Id: Weekly <weekly.test>", "List-Unsubscribe: <https://weekly.test/u>"}

// freshAccount is an account nobody ever sorted: everything sits in INBOX.
func freshAccount(t *testing.T) *m3Env {
	t.Helper()
	e := newBareEnv(t)
	now := time.Now()
	e.deliverAt("INBOX", "news1@weekly.test", "<f-old-news@t>", now.AddDate(0, 0, -90), newsletterHeaders, imap.FlagSeen)
	e.deliverAt("INBOX", "news2@weekly.test", "<f-new-news@t>", now.AddDate(0, 0, -3), newsletterHeaders, imap.FlagSeen)
	e.deliverAt("INBOX", "alice@friends.test", "<f-alice@t>", now.AddDate(0, 0, -3), nil, imap.FlagSeen)
	e.deliverAt("INBOX", "bob@nowhere.test", "<f-bob@t>", now.AddDate(0, 0, -2), nil, imap.FlagSeen)
	e.deliverSent("alice@friends.test", "<f-sent@t>")
	return e
}

func TestItestFreshBacklogFlow(t *testing.T) {
	e := freshAccount(t)

	out, err := e.run("init", "--fresh", "--backlog", "30d", "--dry-run", "--report", "")
	if err != nil {
		t.Fatalf("init dry run: %v\n%s", err, out)
	}
	t.Logf("init --fresh --backlog 30d --dry-run:\n%s", out)
	if !strings.Contains(out, "Backlog: 3 INBOX message(s)") {
		t.Errorf("the dry run should count the 3 messages of the last 30 days:\n%s", out)
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Error("a dry run created the database")
	}

	if out, err = e.run("init", "--fresh", "--backlog", "30d", "--yes", "--report", ""); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	// init itself moved nothing.
	for _, id := range []string{"<f-old-news@t>", "<f-new-news@t>", "<f-alice@t>", "<f-bob@t>"} {
		if !e.has("INBOX", id) {
			t.Fatalf("init moved %s", id)
		}
	}

	out, err = e.run("once", "--dry-run")
	if err != nil {
		t.Fatalf("once --dry-run: %v\n%s", err, out)
	}
	t.Logf("once --dry-run:\n%s", out)
	if !strings.Contains(out, "news2@weekly.test") || strings.Contains(out, "news1@weekly.test") ||
		strings.Contains(out, "alice@friends.test") || strings.Contains(out, "bob@nowhere.test") {
		t.Errorf("only the recent newsletter should be listed:\n%s", out)
	}

	if out, err = e.run("once"); err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	t.Logf("once:\n%s", out)
	if !e.has("SaneNews", "<f-new-news@t>") || e.has("INBOX", "<f-new-news@t>") {
		t.Error("the recent, already read newsletter should have been filed")
	}
	for _, id := range []string{"<f-old-news@t>", "<f-alice@t>", "<f-bob@t>"} {
		if !e.has("INBOX", id) {
			t.Errorf("%s should have stayed in INBOX (outside the window, or someone you wrote to, or no signal)", id)
		}
	}

	if out, err = e.run("undo", "--since", "1h"); err != nil || !strings.Contains(out, "1 restored") {
		t.Fatalf("undo: %v\n%s", err, out)
	}
	if !e.has("INBOX", "<f-new-news@t>") {
		t.Error("undo did not restore the newsletter")
	}
}

func TestItestFreshWithoutBacklogTouchesNothing(t *testing.T) {
	e := freshAccount(t)
	if out, err := e.run("init", "--fresh", "--yes", "--report", ""); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out, err := e.run("once")
	if err != nil || !strings.Contains(out, "0 new") {
		t.Fatalf("once: %v\n%s", err, out)
	}
	if !e.has("INBOX", "<f-new-news@t>") {
		t.Fatal("existing INBOX mail was swept without --backlog")
	}
	// Mail that arrives after init is still filed.
	e.deliverAt("INBOX", "news3@weekly.test", "<f-late-news@t>", time.Now(), newsletterHeaders)
	if out, err = e.run("once"); err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	if !e.has("SaneNews", "<f-late-news@t>") {
		t.Errorf("new mail after init was not filed:\n%s", out)
	}
}

func TestItestAccountFlag(t *testing.T) {
	e := newBareEnv(t)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "")
	t.Setenv("QUIETBOX_IMAP_PASSWORD_ITEST_ACCT", "test")
	dir := filepath.Join(root, "config", "quietbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// An account file has no [state] path: the database must land at <name>.db.
	body := m3Config(e.user, "")
	body = strings.Replace(body, "[state]\npath = \"\"\n", "", 1)
	if err := os.WriteFile(filepath.Join(dir, "itest-acct.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := execRoot(t, "--account", "itest-acct", "init", "--yes", "--report", ""); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	wantDB := filepath.Join(root, "state", "quietbox", "itest-acct.db")
	if _, err := os.Stat(wantDB); err != nil {
		t.Fatalf("database not at %s: %v", wantDB, err)
	}
	out, err := execRoot(t, "--account", "itest-acct", "once")
	if err != nil || !strings.Contains(out, "0 new") {
		t.Fatalf("once: %v\n%s", err, out)
	}
	out, err = execRoot(t, "accounts")
	if err != nil || !strings.Contains(out, "itest-acct") || !strings.Contains(out, e.user) {
		t.Fatalf("accounts: %v\n%s", err, out)
	}
}

func TestItestWrongAccountDatabaseRefused(t *testing.T) {
	a := newM3Env(t)
	other := fmt.Sprintf("other-%d@quietbox.test", time.Now().UnixNano())
	cfgB := filepath.Join(filepath.Dir(a.cfg), "other.toml")
	if err := os.WriteFile(cfgB, []byte(m3Config(other, a.db)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := execRoot(t, "once", "--config", cfgB)
	if err == nil || !strings.Contains(err.Error(), "belongs to") || !strings.Contains(err.Error(), "[state] path") {
		t.Fatalf("err = %v, want the database refused for another account", err)
	}
	// The real owner still works.
	if out, err := a.run("once"); err != nil {
		t.Fatalf("owner: %v\n%s", err, out)
	}
}
