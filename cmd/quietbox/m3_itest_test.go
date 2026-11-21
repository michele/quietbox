//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/store"
)

// M3 tests move mail, so each one gets its own throwaway user: Dovecot accepts
// any user with the test password and gives it an empty mailbox. The seeded
// me@quietbox.test mailbox (counted by other tests) is never touched.

var m3Counter atomic.Int64

type m3Env struct {
	t    *testing.T
	user string
	cfg  string
	db   string
}

func rawLogin(t *testing.T, user string) (*imapclient.Client, func()) {
	t.Helper()
	c, err := imapclient.DialInsecure("127.0.0.1:1143", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(user, "test").Wait(); err != nil {
		_ = c.Close()
		t.Fatal(err)
	}
	return c, func() { _ = c.Logout().Wait(); _ = c.Close() }
}

// deliver appends a message the way a mail server would. Date is now so it is
// inside every lookback.
func (e *m3Env) deliver(folder, from, msgID string, flags ...imap.Flag) {
	e.t.Helper()
	e.deliverBody(folder, from, msgID, "body", flags...)
}

// deliverBody is deliver with a chosen body, e.g. a unique marker that output
// must never contain.
func (e *m3Env) deliverBody(folder, from, msgID, body string, flags ...imap.Flag) {
	e.t.Helper()
	c, done := rawLogin(e.t, e.user)
	defer done()
	now := time.Now().UTC()
	msg := fmt.Sprintf("From: <%s>\r\nTo: <%s>\r\nSubject: m3 test\r\nDate: %s\r\nMessage-ID: %s\r\n\r\n%s\r\n",
		from, e.user, now.Format(time.RFC1123Z), msgID, body)
	cmd := c.Append(folder, int64(len(msg)), &imap.AppendOptions{Flags: flags, Time: now})
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

// deliverSent appends a message to Sent addressed to rcpt.
func (e *m3Env) deliverSent(rcpt, msgID string) {
	e.t.Helper()
	c, done := rawLogin(e.t, e.user)
	defer done()
	now := time.Now().UTC()
	msg := fmt.Sprintf("From: <%s>\r\nTo: <%s>\r\nSubject: re\r\nDate: %s\r\nMessage-ID: %s\r\n\r\nbody\r\n",
		e.user, rcpt, now.Format(time.RFC1123Z), msgID)
	cmd := c.Append("Sent", int64(len(msg)), &imap.AppendOptions{Time: now})
	_, _ = cmd.Write([]byte(msg))
	_ = cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		e.t.Fatal(err)
	}
}

// has reports whether a folder holds a message with this Message-ID.
func (e *m3Env) has(folder, msgID string) bool {
	e.t.Helper()
	c, done := rawLogin(e.t, e.user)
	defer done()
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		e.t.Fatal(err)
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: msgID}}}, nil).Wait()
	if err != nil {
		e.t.Fatal(err)
	}
	return len(found.AllUIDs()) > 0
}

// userMove moves a message the way a mail client would when you drag it.
func (e *m3Env) userMove(from, msgID, to string) {
	e.t.Helper()
	c, done := rawLogin(e.t, e.user)
	defer done()
	if _, err := c.Select(from, nil).Wait(); err != nil {
		e.t.Fatal(err)
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: msgID}}}, nil).Wait()
	if err != nil || len(found.AllUIDs()) != 1 {
		e.t.Fatalf("find %s in %s: %v %v", msgID, from, found, err)
	}
	if _, err := c.Move(imap.UIDSetNum(found.AllUIDs()...), to).Wait(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *m3Env) run(args ...string) (string, error) {
	e.t.Helper()
	return execRoot(e.t, append(args, "--config", e.cfg)...)
}

// newBareEnv creates a throwaway user with the typed folders and a config, but
// no mail and no `init` yet.
func newBareEnv(t *testing.T) *m3Env {
	t.Helper()
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "test")
	dir := t.TempDir()
	e := &m3Env{
		t:    t,
		user: fmt.Sprintf("m3-%d-%d@quietbox.test", time.Now().UnixNano(), m3Counter.Add(1)),
		cfg:  filepath.Join(dir, "config.toml"),
		db:   filepath.Join(dir, "state", "q.db"),
	}
	c, done := rawLogin(t, e.user)
	for _, f := range []string{"SaneLater", "SaneNews", "SaneBulk"} {
		if err := c.Create(f, nil).Wait(); err != nil {
			t.Fatal(err)
		}
	}
	done()
	if err := os.WriteFile(e.cfg, []byte(m3Config(e.user, e.db)), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// m3Config is a config with one folder per type for a throwaway user.
func m3Config(user, db string) string {
	return fmt.Sprintf(`
[imap]
host = "127.0.0.1"
port = 1143
tls = "none"
username = %q

[state]
path = %q

[[folder]]
name = "INBOX"
type = "important"
[[folder]]
name = "SaneLater"
type = "later"
[[folder]]
name = "SaneNews"
type = "newsletter"
[[folder]]
name = "SaneBulk"
type = "bulk"
`, user, db)
}

// newM3Env creates the user's folders and some filing history, writes a config
// and runs `quietbox init`, so the next mail that arrives is "new".
func newM3Env(t *testing.T) *m3Env {
	t.Helper()
	e := newBareEnv(t)
	dir := filepath.Dir(e.cfg)

	// History: where each sender's mail has been filed so far.
	for i := 1; i <= 3; i++ {
		e.deliver("SaneNews", "writer@news.test", fmt.Sprintf("<hist-news-%d@t>", i))
		e.deliver("SaneBulk", "store@shop.test", fmt.Sprintf("<hist-bulk-%d@t>", i))
	}
	e.deliver("INBOX", "alice@friends.test", "<hist-alice-1@t>")
	e.deliver("INBOX", "alice@friends.test", "<hist-alice-2@t>")
	e.deliverSent("alice@friends.test", "<hist-sent-1@t>")

	if out, err := e.run("init", "--yes", "--report", filepath.Join(dir, "report.csv")); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return e
}

// deliverBatch is the mail that arrives after init.
func (e *m3Env) deliverBatch() {
	e.deliver("INBOX", "writer@news.test", "<new-news@t>")
	e.deliver("INBOX", "store@shop.test", "<new-bulk@t>")
	e.deliver("INBOX", "stranger@nowhere.test", "<new-stranger@t>")
	e.deliver("INBOX", "alice@friends.test", "<new-alice@t>")
	e.deliver("INBOX", "writer@news.test", "<new-seen@t>", imap.FlagSeen)
}

func (e *m3Env) assertFiledAsExpected() {
	e.t.Helper()
	for id, folder := range map[string]string{"<new-news@t>": "SaneNews", "<new-bulk@t>": "SaneBulk"} {
		if !e.has(folder, id) || e.has("INBOX", id) {
			e.t.Errorf("%s should be in %s and gone from INBOX", id, folder)
		}
	}
	for _, id := range []string{"<new-stranger@t>", "<new-alice@t>", "<new-seen@t>"} {
		if !e.has("INBOX", id) {
			e.t.Errorf("%s should have stayed in INBOX", id)
		}
	}
}

func TestM3OnceMovesNewMailOnly(t *testing.T) {
	e := newM3Env(t)
	e.deliverBatch()

	out, err := e.run("once", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	t.Logf("once --dry-run:\n%s", out)
	if !strings.Contains(out, "would move") || !strings.Contains(out, "writer@news.test") || !strings.Contains(out, "nothing was changed") {
		t.Errorf("dry-run output:\n%s", out)
	}
	if !e.has("INBOX", "<new-news@t>") || !e.has("INBOX", "<new-bulk@t>") {
		t.Fatal("dry run moved mail")
	}
	if strings.Contains(out, "m3 test") {
		t.Error("output leaks a subject")
	}

	out, err = e.run("once")
	if err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	t.Logf("once:\n%s", out)
	e.assertFiledAsExpected()

	// A second pass has nothing new.
	out, err = e.run("once")
	if err != nil || !strings.Contains(out, "0 new") {
		t.Fatalf("second once: %v\n%s", err, out)
	}

	// The audit log recorded both moves, completed, with their destination.
	st, err := store.OpenExisting(e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, _ := st.Audit(context.Background())
	moves := 0
	for _, r := range rows {
		if r.Action == store.ActionMove {
			moves++
			if r.Status != store.AuditDone || r.DestUID == 0 || r.FromFolder != "INBOX" {
				t.Errorf("audit row = %+v", r)
			}
		}
	}
	if moves != 2 {
		t.Errorf("%d move rows, want 2", moves)
	}
}

func TestM3UndoRestoresAndIsNotRefiled(t *testing.T) {
	e := newM3Env(t)
	e.deliverBatch()
	if out, err := e.run("once"); err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}

	out, err := e.run("undo", "--since", "1h", "--dry-run")
	if err != nil {
		t.Fatalf("undo dry run: %v\n%s", err, out)
	}
	t.Logf("undo --dry-run:\n%s", out)
	if e.has("INBOX", "<new-news@t>") {
		t.Fatal("undo --dry-run moved mail")
	}

	out, err = e.run("undo", "--since", "1h")
	if err != nil {
		t.Fatalf("undo: %v\n%s", err, out)
	}
	t.Logf("undo:\n%s", out)
	if !strings.Contains(out, "2 restored") || !e.has("INBOX", "<new-news@t>") || !e.has("INBOX", "<new-bulk@t>") {
		t.Fatalf("undo did not restore both messages:\n%s", out)
	}

	// Restored mail is new to INBOX again, but quietbox must not file it twice.
	out, err = e.run("once")
	if err != nil {
		t.Fatalf("once after undo: %v\n%s", err, out)
	}
	if !e.has("INBOX", "<new-news@t>") || !e.has("INBOX", "<new-bulk@t>") {
		t.Errorf("restored mail was filed again:\n%s", out)
	}
}

func TestM3DraggedBackMessageIsNotMovedAgain(t *testing.T) {
	e := newM3Env(t)
	e.deliverBatch()
	if out, err := e.run("once"); err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	e.userMove("SaneNews", "<new-news@t>", "INBOX") // you disagree and drag it back
	out, err := e.run("once")
	if err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	if !e.has("INBOX", "<new-news@t>") || e.has("SaneNews", "<new-news@t>") {
		t.Errorf("a message you dragged back was filed again:\n%s", out)
	}
}

func TestM3UndoSkipsMessageYouMovedYourself(t *testing.T) {
	e := newM3Env(t)
	e.deliverBatch()
	if out, err := e.run("once"); err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	e.userMove("SaneBulk", "<new-bulk@t>", "SaneLater")
	out, err := e.run("undo", "--since", "1h")
	if err != nil {
		t.Fatalf("undo: %v\n%s", err, out)
	}
	t.Logf("undo:\n%s", out)
	if !strings.Contains(out, "1 restored") || !strings.Contains(out, "1 skipped") {
		t.Errorf("undo output:\n%s", out)
	}
	if !e.has("SaneLater", "<new-bulk@t>") || e.has("INBOX", "<new-bulk@t>") {
		t.Error("a message you filed yourself was moved")
	}
}

func TestM3RunFilesMailAsItArrives(t *testing.T) {
	e := newM3Env(t)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"run", "--config", e.cfg})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	defer cancel()

	time.Sleep(1500 * time.Millisecond) // first pass, then IDLE
	e.deliver("INBOX", "writer@news.test", "<run-news@t>")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && !e.has("SaneNews", "<run-news@t>") {
		time.Sleep(200 * time.Millisecond)
	}
	if !e.has("SaneNews", "<run-news@t>") {
		t.Fatalf("run did not file the mail that arrived while it was running\n%s", out.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run after cancel = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestM3OnceRefusesAnotherInstance(t *testing.T) {
	e := newM3Env(t)
	e.deliverBatch()
	// Another quietbox holds the pass lock (a `run` pass, an `undo`...) and does
	// not let go: once, undo and init wait --lock-timeout, then fail, changing nothing.
	lock := e.db + ".lock"
	release, err := engine.AcquireLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	want := "another quietbox is still running on this mailbox after waiting 1s (lock file " + lock + ")"
	for _, args := range [][]string{
		{"once"},
		{"undo", "--since", "1h"},
		{"init", "--yes", "--report", ""},
	} {
		start := time.Now()
		out, err := e.run(append(args, "--lock-timeout", "1s")...)
		waited := time.Since(start)
		t.Logf("%v alongside a held pass lock (%v): %v\n%s", args, waited.Round(10*time.Millisecond), err, out)
		if err == nil || err.Error() != want {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
		if waited < time.Second {
			t.Errorf("%v gave up after %v, before --lock-timeout", args, waited)
		}
	}
	if !e.has("INBOX", "<new-news@t>") || !e.has("INBOX", "<new-bulk@t>") {
		t.Error("mail was moved while another quietbox held the pass lock")
	}
}

// Once the other quietbox lets go, a waiting once runs, saying it waited.
func TestM3OnceWaitsForThePassLock(t *testing.T) {
	e := newM3Env(t)
	e.deliverBatch()
	release, err := engine.AcquireLock(e.db + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		release()
	}()
	start := time.Now()
	out, err := e.run("once", "--lock-timeout", "10s")
	t.Logf("once after waiting %v:\n%s", time.Since(start).Round(10*time.Millisecond), out)
	if err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	if strings.Count(out, "waiting for another quietbox...") != 1 || time.Since(start) < 1400*time.Millisecond {
		t.Errorf("once did not wait for the lock (or did not say so):\n%s", out)
	}
	e.assertFiledAsExpected()
}
