//go:build integration

package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/michele/quietbox/internal/store"
)

// M4 tests: a second IMAP connection (m3Env.userMove) plays the user moving
// mail by hand; quietbox must learn from it, and never from its own moves.

// syncBuffer is an output buffer `run` can write to while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startRun starts `quietbox run` and waits until its first pass is done. The
// returned stop cancels it and waits for it to return.
func (e *m3Env) startRun() (*syncBuffer, func()) {
	e.t.Helper()
	root := newRootCmd()
	out := &syncBuffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"run", "--config", e.cfg})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				e.t.Errorf("run after cancel = %v\n%s", err, out.String())
			}
		case <-time.After(15 * time.Second):
			e.t.Errorf("run did not stop\n%s", out.String())
		}
	}
	e.t.Cleanup(stop)
	if !waitFor(e.t, 15*time.Second, func() bool { return strings.Contains(out.String(), "pass done") }) {
		stop()
		e.t.Fatalf("run did not finish its first pass\n%s", out.String())
	}
	return out, stop
}

// route returns the ROUTE and SOURCE columns `quietbox senders` prints for
// addr, or "", "" when the sender is not listed.
func (e *m3Env) route(addr string) (folder, source string) {
	e.t.Helper()
	out, err := e.run("senders")
	if err != nil {
		e.t.Fatalf("senders: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[0] == addr {
			return f[1], f[2]
		}
	}
	return "", ""
}

// uidOf is the UID of the message with this Message-ID in folder.
func (e *m3Env) uidOf(folder, msgID string) uint32 {
	e.t.Helper()
	c, done := rawLogin(e.t, e.user)
	defer done()
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		e.t.Fatal(err)
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: msgID}}}, nil).Wait()
	if err != nil || len(found.AllUIDs()) != 1 {
		e.t.Fatalf("find %s in %s: %v %v", msgID, folder, found, err)
	}
	return uint32(found.AllUIDs()[0])
}

func (e *m3Env) once() string {
	e.t.Helper()
	out, err := e.run("once")
	if err != nil {
		e.t.Fatalf("once: %v\n%s", err, out)
	}
	return out
}

func (e *m3Env) assertIn(folder, msgID string) {
	e.t.Helper()
	if !e.has(folder, msgID) {
		e.t.Errorf("%s should be in %s", msgID, folder)
	}
	if folder != "INBOX" && e.has("INBOX", msgID) {
		e.t.Errorf("%s should have left INBOX", msgID)
	}
}

// trainingRows is the training log for a sender, newest first.
func (e *m3Env) trainingRows(sender string) []store.TrainingRow {
	e.t.Helper()
	st, err := store.OpenExisting(e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.TrainingFor(context.Background(), sender)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func TestM4UserFilingTrainsSender(t *testing.T) {
	e := newM3Env(t)
	// A stranger's mail matches nothing, so it stays in INBOX.
	e.deliver("INBOX", "stranger@nowhere.test", "<s-1@t>")
	// writer@news.test is learned to SaneNews; quietbox files this one there.
	e.deliver("INBOX", "writer@news.test", "<w-1@t>")
	out := e.once()
	t.Logf("once:\n%s", out)
	e.assertIn("INBOX", "<s-1@t>")
	e.assertIn("SaneNews", "<w-1@t>")
	if strings.Contains(out, "trained") {
		t.Errorf("quietbox trained from its own move:\n%s", out)
	}

	// You file the stranger into SaneLater, and move writer's message (filed
	// by quietbox) on from SaneNews to SaneBulk.
	e.userMove("INBOX", "<s-1@t>", "SaneLater")
	e.userMove("SaneNews", "<w-1@t>", "SaneBulk")
	out = e.once()
	t.Logf("once after your moves:\n%s", out)
	for _, want := range []string{
		"trained stranger@nowhere.test: you moved a message to SaneLater",
		"trained writer@news.test: you moved a message to SaneBulk (route was SaneNews)",
		"trained 2 sender(s) from your moves",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("once output lacks %q:\n%s", want, out)
		}
	}
	if f, s := e.route("stranger@nowhere.test"); f != "SaneLater" || s != "trained" {
		t.Errorf("stranger route = %s %s, want SaneLater trained", f, s)
	}
	if f, s := e.route("writer@news.test"); f != "SaneBulk" || s != "trained" {
		t.Errorf("writer route = %s %s, want SaneBulk trained", f, s)
	}

	// Their next mail goes where you put the last one.
	e.deliver("INBOX", "stranger@nowhere.test", "<s-2@t>")
	e.deliver("INBOX", "writer@news.test", "<w-2@t>")
	out = e.once()
	t.Logf("once with new mail:\n%s", out)
	e.assertIn("SaneLater", "<s-2@t>")
	e.assertIn("SaneBulk", "<w-2@t>")
	if strings.Contains(out, "trained") {
		t.Errorf("a pass with no moves of yours trained:\n%s", out)
	}
	// The messages you moved stay where you put them.
	e.assertIn("SaneLater", "<s-1@t>")
	e.assertIn("SaneBulk", "<w-1@t>")
}

func TestM4ReturnToInboxKeepsSender(t *testing.T) {
	e := newM3Env(t)
	e.deliver("INBOX", "writer@news.test", "<r-1@t>")
	e.once()
	e.assertIn("SaneNews", "<r-1@t>")

	// You disagree and drag it back.
	e.userMove("SaneNews", "<r-1@t>", "INBOX")
	out := e.once()
	t.Logf("once after the drag back:\n%s", out)
	if !strings.Contains(out, "trained writer@news.test: you moved a message from SaneNews to INBOX") {
		t.Errorf("once output:\n%s", out)
	}
	e.assertIn("INBOX", "<r-1@t>")
	if f, s := e.route("writer@news.test"); f != "INBOX" || s != "trained" {
		t.Errorf("writer route = %s %s, want INBOX trained", f, s)
	}

	// The sender's next newsletter stays in INBOX.
	e.deliver("INBOX", "writer@news.test", "<r-2@t>")
	out = e.once()
	t.Logf("once with new mail:\n%s", out)
	e.assertIn("INBOX", "<r-2@t>")
	if !strings.Contains(out, "1 stayed in INBOX") {
		t.Errorf("once output:\n%s", out)
	}
	if rows := e.trainingRows("writer@news.test"); len(rows) != 1 || rows[0].Folder != "INBOX" || rows[0].Source != store.TrainMove {
		t.Errorf("training rows = %+v, want one move to INBOX", rows)
	}
}

func TestM4UndoIsNotTraining(t *testing.T) {
	e := newM3Env(t)
	e.deliver("INBOX", "writer@news.test", "<u-1@t>")
	e.deliver("INBOX", "store@shop.test", "<u-2@t>")
	e.once()
	e.assertIn("SaneNews", "<u-1@t>")
	e.assertIn("SaneBulk", "<u-2@t>")

	out, err := e.run("undo", "--since", "1h")
	if err != nil || !strings.Contains(out, "2 restored") {
		t.Fatalf("undo: %v\n%s", err, out)
	}
	out = e.once()
	t.Logf("once after undo:\n%s", out)
	if strings.Contains(out, "trained") {
		t.Errorf("undo was read as training:\n%s", out)
	}
	e.assertIn("INBOX", "<u-1@t>")
	e.assertIn("INBOX", "<u-2@t>")
	for addr, folder := range map[string]string{"writer@news.test": "SaneNews", "store@shop.test": "SaneBulk"} {
		if f, s := e.route(addr); f != folder || s != "learned" {
			t.Errorf("%s route = %s %s, want %s learned", addr, f, s, folder)
		}
		if rows := e.trainingRows(addr); len(rows) != 0 {
			t.Errorf("%s has training rows after undo: %+v", addr, rows)
		}
	}
	// The learned routes still file the senders' next mail.
	e.deliver("INBOX", "writer@news.test", "<u-3@t>")
	e.once()
	e.assertIn("SaneNews", "<u-3@t>")
}

func TestM4SendersSetAndUnset(t *testing.T) {
	e := newM3Env(t)

	if out, err := e.run("senders", "set", "stranger@nowhere.test", "NoSuchFolder"); err == nil || !strings.Contains(err.Error(), "valid folders") {
		t.Errorf("set to an unknown folder: err = %v\n%s", err, out)
	}

	out, err := e.run("senders", "set", "Stranger@Nowhere.test", "SaneBulk")
	if err != nil || !strings.Contains(out, "stranger@nowhere.test -> SaneBulk (trained)") {
		t.Fatalf("senders set: %v\n%s", err, out)
	}
	if f, s := e.route("stranger@nowhere.test"); f != "SaneBulk" || s != "trained" {
		t.Errorf("route = %s %s, want SaneBulk trained", f, s)
	}
	if rows := e.trainingRows("stranger@nowhere.test"); len(rows) != 1 || rows[0].Source != store.TrainManual {
		t.Errorf("training rows = %+v, want one manual row", rows)
	}
	e.deliver("INBOX", "stranger@nowhere.test", "<set-1@t>")
	e.once()
	e.assertIn("SaneBulk", "<set-1@t>")

	out, err = e.run("senders", "unset", "stranger@nowhere.test")
	if err != nil || !strings.Contains(out, "stranger@nowhere.test: trained route removed") {
		t.Fatalf("senders unset: %v\n%s", err, out)
	}
	if f, _ := e.route("stranger@nowhere.test"); f != "" {
		t.Errorf("stranger still has a route %s after unset", f)
	}
	out, err = e.run("senders", "unset", "stranger@nowhere.test")
	if err != nil || !strings.Contains(out, "no trained route for stranger@nowhere.test") {
		t.Errorf("second unset: %v\n%s", err, out)
	}
	e.deliver("INBOX", "stranger@nowhere.test", "<set-2@t>")
	e.once()
	e.assertIn("INBOX", "<set-2@t>")

	// Unsetting a trained route over a learned one drops the row; the learned
	// route comes back at the next init/learn.
	if out, err := e.run("senders", "set", "writer@news.test", "SaneLater"); err != nil {
		t.Fatalf("set writer: %v\n%s", err, out)
	}
	if out, err := e.run("senders", "unset", "writer@news.test"); err != nil {
		t.Fatalf("unset writer: %v\n%s", err, out)
	}
	if f, _ := e.route("writer@news.test"); f != "" {
		t.Errorf("writer route after unset = %s, want none until learn", f)
	}
	if out, err := e.run("learn", "--yes", "--report", ""); err != nil {
		t.Fatalf("learn: %v\n%s", err, out)
	}
	if f, s := e.route("writer@news.test"); f != "SaneNews" || s != "learned" {
		t.Errorf("writer route after learn = %s %s, want SaneNews learned", f, s)
	}

	// While `run` holds the lock, `senders set` waits for it, then completes.
	_, stop := e.startRun()
	setOut := &syncBuffer{}
	setDone := make(chan error, 1)
	go func() {
		root := newRootCmd()
		root.SetOut(setOut)
		root.SetErr(setOut)
		root.SetArgs([]string{"senders", "set", "late@nowhere.test", "SaneLater", "--config", e.cfg})
		setDone <- root.Execute()
	}()
	select {
	case err := <-setDone:
		t.Fatalf("senders set finished while run held the lock: %v\n%s", err, setOut.String())
	case <-time.After(1500 * time.Millisecond):
	}
	if !strings.Contains(setOut.String(), "waiting for another quietbox") {
		t.Errorf("senders set did not say it is waiting:\n%s", setOut.String())
	}
	stop()
	select {
	case err := <-setDone:
		if err != nil {
			t.Fatalf("senders set after run stopped: %v\n%s", err, setOut.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("senders set did not finish after run stopped\n%s", setOut.String())
	}
	t.Logf("senders set while run was up:\n%s", setOut.String())
	if !strings.Contains(setOut.String(), "late@nowhere.test -> SaneLater (trained)") {
		t.Errorf("senders set output:\n%s", setOut.String())
	}
	if f, s := e.route("late@nowhere.test"); f != "SaneLater" || s != "trained" {
		t.Errorf("late route = %s %s, want SaneLater trained", f, s)
	}
}

func TestM4ExplainOutput(t *testing.T) {
	e := newM3Env(t)
	e.deliver("INBOX", "stranger@nowhere.test", "<x-1@t>")
	e.once()
	e.userMove("INBOX", "<x-1@t>", "SaneLater")
	e.once()
	e.deliver("INBOX", "stranger@nowhere.test", "<x-2@t>")

	// By Message-ID (angle brackets optional), in INBOX before quietbox saw it.
	out, err := e.run("explain", "x-2@t")
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	t.Logf("explain x-2@t:\n%s", out)
	for _, want := range []string{
		"would decide now",
		"sender:     stranger@nowhere.test",
		"message-id: <x-2@t>",
		"decision:   SaneLater",
		"rule:       sender",
		"route:      SaneLater (trained)",
		"training:   latest: stranger@nowhere.test -> SaneLater (move)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "m3 test") || strings.Contains(out, "body") {
		t.Errorf("explain leaks the subject or body:\n%s", out)
	}
	// Explain only reads: the message is still in INBOX.
	e.assertIn("INBOX", "<x-2@t>")

	// By UID with --folder, for a message quietbox filed: the audit row shows.
	e.once()
	e.assertIn("SaneLater", "<x-2@t>")
	uid := e.uidOf("SaneLater", "<x-2@t>")
	out, err = e.run("explain", "--folder", "SaneLater", strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		t.Fatalf("explain by UID: %v\n%s", err, out)
	}
	t.Logf("explain --folder SaneLater %d:\n%s", uid, out)
	for _, want := range []string{
		"folder:     SaneLater (UID " + strconv.FormatUint(uint64(uid), 10) + ")",
		"audit:",
		"move INBOX -> SaneLater, done",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "m3 test") {
		t.Errorf("explain leaks the subject:\n%s", out)
	}
}

func TestM4RunTrainsOnNextDelivery(t *testing.T) {
	e := newM3Env(t)
	e.deliver("INBOX", "stranger@nowhere.test", "<run-1@t>")
	out, stop := e.startRun() // its first pass leaves the stranger in INBOX
	defer stop()
	e.assertIn("INBOX", "<run-1@t>")

	// You file the message by hand while run is idle on INBOX, then the
	// stranger writes again: that delivery wakes run, which learns from your
	// move before it classifies the new mail.
	e.userMove("INBOX", "<run-1@t>", "SaneLater")
	e.deliver("INBOX", "stranger@nowhere.test", "<run-2@t>")
	if !waitFor(t, 20*time.Second, func() bool { return e.has("SaneLater", "<run-2@t>") }) {
		t.Fatalf("run did not file the new mail by the trained route\n%s", out.String())
	}
	e.assertIn("SaneLater", "<run-2@t>")
	if !waitFor(t, 5*time.Second, func() bool {
		return strings.Contains(out.String(), "trained stranger@nowhere.test: you moved a message to SaneLater")
	}) {
		t.Errorf("run did not report the training\n%s", out.String())
	}
	stop()
	t.Logf("run output:\n%s", out.String())
	if f, s := e.route("stranger@nowhere.test"); f != "SaneLater" || s != "trained" {
		t.Errorf("route = %s %s, want SaneLater trained", f, s)
	}
}
