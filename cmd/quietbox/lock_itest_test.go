//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/store"
)

// Lock tests: `run` holds its daemon lock (<state>.lock.run) for life but the
// pass lock (<state>.lock) only while a pass executes, so other commands get
// in between passes, a second run is refused, and a killed run strands nothing.

// mailFolders are the folders a throwaway m3 user has.
var mailFolders = []string{"INBOX", "SaneLater", "SaneNews", "SaneBulk"}

// messageIDs maps every Message-ID in folder to how many copies it holds, in
// one connection (has() logs in per call, too slow for many messages).
func (e *m3Env) messageIDs(folder string) map[string]int {
	e.t.Helper()
	c, done := rawLogin(e.t, e.user)
	defer done()
	sel, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		e.t.Fatal(err)
	}
	ids := map[string]int{}
	if sel.NumMessages == 0 {
		return ids
	}
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}, Peek: true}
	var all imap.SeqSet
	all.AddRange(1, 0) // 1:*
	msgs, err := c.Fetch(all, &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		e.t.Fatal(err)
	}
	for _, m := range msgs {
		for _, line := range strings.Split(string(m.FindBodySection(section)), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Message-ID") {
				ids[strings.TrimSpace(v)]++
			}
		}
	}
	return ids
}

// auditRows is the whole audit log.
func (e *m3Env) auditRows() []store.AuditRow {
	e.t.Helper()
	st, err := store.OpenExisting(e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.Audit(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

// assertAuditClean checks that no message was moved twice and no move is left
// half done. It returns the number of move rows.
func (e *m3Env) assertAuditClean() int {
	e.t.Helper()
	perKey := map[string]int{}
	moves := 0
	for _, r := range e.auditRows() {
		if r.Status == store.AuditPlanned {
			e.t.Errorf("audit row left planned: %+v", r)
		}
		if r.Status == store.AuditFailed {
			e.t.Errorf("audit row failed: %+v", r)
		}
		if r.Action == store.ActionMove {
			moves++
			perKey[r.MessageKey]++
		}
	}
	for k, n := range perKey {
		if n != 1 {
			e.t.Errorf("message key %s has %d move rows, want 1", k, n)
		}
	}
	return moves
}

func passCount(out *syncBuffer) int { return strings.Count(out.String(), "pass done") }

// runCtx runs a command with a deadline, so a regression that blocks fails
// the test instead of hanging it.
func (e *m3Env) runCtx(within time.Duration, args ...string) (string, error) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	return execRootCtx(e.t, ctx, append(args, "--config", e.cfg)...)
}

func TestLockSendersSetCompletesWhileRunIdles(t *testing.T) {
	e := newM3Env(t)
	out, stop := e.startRun()
	defer stop()

	start := time.Now()
	setOut, err := e.runCtx(20*time.Second, "senders", "set", "idle@nowhere.test", "SaneLater")
	took := time.Since(start)
	t.Logf("senders set while run idles (%v):\n%s", took.Round(10*time.Millisecond), setOut)
	if err != nil || !strings.Contains(setOut, "idle@nowhere.test -> SaneLater (trained)") {
		t.Fatalf("senders set: %v\n%s", err, setOut)
	}
	if took > 3*time.Second || strings.Contains(setOut, "waiting for another quietbox") {
		t.Errorf("senders set waited for an idle run (%v):\n%s", took, setOut)
	}

	// The same daemon, still running, files the sender's next mail by the new route.
	passes := passCount(out)
	e.deliver("INBOX", "idle@nowhere.test", "<idle-1@t>")
	if !waitFor(t, 20*time.Second, func() bool { return e.has("SaneLater", "<idle-1@t>") }) {
		t.Fatalf("run did not file the new mail by the route set while it idled\n%s", out.String())
	}
	e.assertIn("SaneLater", "<idle-1@t>")
	// The move is visible before the daemon prints "pass done" (it still
	// writes the audit row and cursors), so wait for the line, not just check.
	if !waitFor(t, 10*time.Second, func() bool { return passCount(out) > passes }) {
		t.Errorf("no run pass after the delivery: someone else filed it?\n%s", out.String())
	}
	stop()
	t.Logf("run output:\n%s", out.String())
}

func TestLockSecondRunRefusedWhileFirstIdles(t *testing.T) {
	e := newM3Env(t)
	out, stop := e.startRun()
	defer stop()

	start := time.Now()
	second, err := e.runCtx(10*time.Second, "run")
	t.Logf("second run (%v): %v\n%s", time.Since(start).Round(10*time.Millisecond), err, second)
	want := "another quietbox run is already running on this mailbox (lock file " + e.db + ".lock.run)"
	if err == nil || err.Error() != want {
		t.Fatalf("second run: err = %v, want %q", err, want)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the refusal took %v", time.Since(start))
	}
	// The first run is unaffected: it still files new mail.
	e.deliver("INBOX", "writer@news.test", "<second-1@t>")
	if !waitFor(t, 20*time.Second, func() bool { return e.has("SaneNews", "<second-1@t>") }) {
		t.Fatalf("the first run stopped filing after the refusal\n%s", out.String())
	}
}

func TestLockOnceAndUndoWorkWhileRunIdles(t *testing.T) {
	e := newM3Env(t)
	out, stop := e.startRun()
	defer stop()

	// Nothing new: once gets in at once.
	start := time.Now()
	o, err := e.runCtx(20*time.Second, "once", "--lock-timeout", "5s")
	t.Logf("once while run idles (%v):\n%s", time.Since(start).Round(10*time.Millisecond), o)
	if err != nil || !strings.Contains(o, "0 new") || strings.Contains(o, "waiting for another quietbox") {
		t.Fatalf("once while run idles: %v\n%s", err, o)
	}

	// New mail wakes the daemon too: whichever of the two gets the pass lock
	// first files it, the other finds it already handled. Nothing is moved twice.
	e.deliver("INBOX", "writer@news.test", "<ou-news@t>")
	e.deliver("INBOX", "store@shop.test", "<ou-bulk@t>")
	o, err = e.runCtx(30*time.Second, "once")
	t.Logf("once with new mail while run is up:\n%s", o)
	if err != nil {
		t.Fatalf("once: %v\n%s", err, o)
	}
	if !waitFor(t, 20*time.Second, func() bool { return e.has("SaneNews", "<ou-news@t>") && e.has("SaneBulk", "<ou-bulk@t>") }) {
		t.Fatalf("mail was not filed\n%s", out.String())
	}
	e.assertIn("SaneNews", "<ou-news@t>")
	e.assertIn("SaneBulk", "<ou-bulk@t>")
	if n := e.assertAuditClean(); n != 2 {
		t.Errorf("%d move rows, want 2", n)
	}

	// undo while run idles restores both; the daemon wakes on the restored
	// mail and must neither file it again nor train on it.
	o, err = e.runCtx(30*time.Second, "undo", "--since", "1h")
	t.Logf("undo while run is up:\n%s", o)
	if err != nil || !strings.Contains(o, "2 restored") {
		t.Fatalf("undo: %v\n%s", err, o)
	}
	// A sentinel delivered after the undo: once the daemon has filed it, it
	// has also passed over the restored mail (lower UIDs in INBOX).
	passes := passCount(out)
	e.deliver("INBOX", "writer@news.test", "<ou-sentinel@t>")
	if !waitFor(t, 20*time.Second, func() bool { return e.has("SaneNews", "<ou-sentinel@t>") }) {
		t.Fatalf("run did not file the sentinel\n%s", out.String())
	}
	if !waitFor(t, 10*time.Second, func() bool { return passCount(out) > passes }) {
		t.Errorf("no run pass after the undo\n%s", out.String())
	}
	e.assertIn("INBOX", "<ou-news@t>")
	e.assertIn("INBOX", "<ou-bulk@t>")
	for _, addr := range []string{"writer@news.test", "store@shop.test"} {
		if rows := e.trainingRows(addr); len(rows) != 0 {
			t.Errorf("%s has training rows after undo: %+v", addr, rows)
		}
	}
	if n := e.assertAuditClean(); n != 3 {
		t.Errorf("%d move rows, want 3 (two undone, the sentinel)", n)
	}
	stop() // Run has returned: every line it will print is in out
	if strings.Contains(out.String(), "trained") {
		t.Errorf("run trained on the undo:\n%s", out.String())
	}
	t.Logf("run output:\n%s", out.String())
}

func TestLockStress(t *testing.T) {
	e := newM3Env(t)
	out, stop := e.startRun()
	defer stop()

	const span = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), span)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 1000)
	var onces, sets int
	lastRoute := "" // the route flip@ was left with by the last set/unset ("" = none)
	// Each command gets a deadline, so a lock regression fails instead of hanging.
	cmdCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 60*time.Second)
	}
	wg.Add(2)
	go func() { // a cron gone wild: once after once
		defer wg.Done()
		for ctx.Err() == nil {
			c, done := cmdCtx()
			o, err := execRootCtx(t, c, "once", "--config", e.cfg)
			done()
			if err != nil {
				errs <- fmt.Errorf("once: %v\n%s", err, o)
				return
			}
			onces++
		}
	}()
	go func() { // you changing your mind about flip@ over and over
		defer wg.Done()
		folders := []string{"SaneLater", "SaneBulk"}
		for i := 0; ctx.Err() == nil; i++ {
			args, route := []string{"senders", "set", "flip@nowhere.test", folders[i%2]}, folders[i%2]
			if i%3 == 2 {
				args, route = []string{"senders", "unset", "flip@nowhere.test"}, ""
			}
			c, done := cmdCtx()
			o, err := execRootCtx(t, c, append(args, "--config", e.cfg)...)
			done()
			if err != nil {
				errs <- fmt.Errorf("%v: %v\n%s", args, err, o)
				return
			}
			lastRoute = route
			sets++
		}
	}()
	// Mail keeps arriving, waking the daemon each time.
	senders := []string{"writer@news.test", "store@shop.test", "stranger@nowhere.test", "flip@nowhere.test"}
	var ids []string
	for i := 0; ctx.Err() == nil; i++ {
		id := fmt.Sprintf("<stress-%d@t>", i)
		e.deliver("INBOX", senders[i%len(senders)], id)
		ids = append(ids, id)
		time.Sleep(50 * time.Millisecond)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	// The pass lock is polled, not fair: while the set/unset loop retakes it in
	// a tight loop, how many passes the daemon wins in 5 s is luck, and on a
	// slow or single-CPU machine it can be none (the documented limitation, see
	// docs/PLAN.md, M4.1). So the number of passes during the stress is only
	// reported. What must hold is liveness: once the contention stops, the
	// daemon (nothing else is running yet) files new mail by itself.
	duringStress := passCount(out)
	const sentinel = "<stress-sentinel@t>"
	e.deliver("INBOX", "writer@news.test", sentinel)
	if !waitFor(t, 30*time.Second, func() bool { return e.has("SaneNews", sentinel) }) {
		t.Fatalf("the daemon did not file mail delivered after the stress\n%s", out.String())
	}
	if !waitFor(t, 10*time.Second, func() bool { return passCount(out) > 1 }) {
		t.Errorf("the daemon filed the sentinel but reported no pass beyond its first\n%s", out.String())
	}
	// Settle: a last once files whatever the daemon has not reached yet.
	if o, err := e.runCtx(30*time.Second, "once"); err != nil {
		t.Fatalf("final once: %v\n%s", err, o)
	}
	stop()
	t.Logf("%d deliveries, %d once runs, %d senders set/unset, %d run passes during the stress, %d in all",
		len(ids), onces, sets, duringStress, passCount(out))
	// No write was lost: flip@'s route is what the last set/unset left.
	if folder, source := e.route("flip@nowhere.test"); lastRoute == "" && source == "trained" || lastRoute != "" && (folder != lastRoute || source != "trained") {
		t.Errorf("flip@ route is %q (%s), the last set/unset left %q", folder, source, lastRoute)
	}

	where := map[string][]string{}
	for _, f := range mailFolders {
		for id, n := range e.messageIDs(f) {
			for range n {
				where[id] = append(where[id], f)
			}
		}
	}
	// Where flip@'s mail went depends on the route the pass that filed it read.
	// A pass reads the routes once, at its start, under the pass lock, and set/
	// unset write under the same lock, so every flip@ message filed by one pass
	// gets the same route. The set loop cycles SaneLater, SaneBulk, unset in
	// ~10 ms steps and starves the 200 ms lock polls of once and run (the lock
	// is not fair), so a few passes file large batches and the totals are skewed
	// and vary from run to run (e.g. INBOX:16 SaneBulk:6 SaneLater:0). What must
	// hold: flip@ mail is filed only by its hand-set route (the move row says so
	// and names the folder it is in) or, with no route, stays in INBOX (it is a
	// stranger otherwise) with no move row.
	flipMoves := map[string]store.AuditRow{}
	for _, r := range e.auditRows() {
		if r.Action == store.ActionMove {
			flipMoves[strings.TrimPrefix(r.MessageKey, "m:")] = r
		}
	}
	flips := map[string]int{}
	for i, id := range ids {
		got := where[id]
		if len(got) != 1 {
			t.Errorf("%s is in %v, want exactly one folder", id, got)
			continue
		}
		if i%len(senders) == 3 {
			flips[got[0]]++
			r, moved := flipMoves[id]
			switch got[0] {
			case "INBOX":
				if moved {
					t.Errorf("%s (flip@) is in INBOX but has a move row %+v", id, r)
				}
			case "SaneLater", "SaneBulk":
				if !moved || r.ToFolder != got[0] || !strings.Contains(r.Reason, "sender route to "+got[0]+" (set by hand)") {
					t.Errorf("%s (flip@) is in %s, move row %+v: not by its hand-set route", id, got[0], r)
				}
			default:
				t.Errorf("%s (flip@) is in %s: never a route it had", id, got[0])
			}
		}
		want := map[int]string{0: "SaneNews", 1: "SaneBulk", 2: "INBOX"}[i%len(senders)]
		if want != "" && got[0] != want {
			t.Errorf("%s (%s) is in %s, want %s", id, senders[i%len(senders)], got[0], want)
		}
	}
	t.Logf("flip@nowhere.test mail ended in %v", flips)
	moves := e.assertAuditClean()
	t.Logf("%d move rows, no key moved twice, none left planned", moves)
	if moves == 0 {
		t.Error("nothing was moved: the stress did not exercise anything")
	}
	if strings.Contains(out.String(), "error:") || strings.Contains(out.String(), "warning:") {
		t.Errorf("run reported trouble:\n%s", out.String())
	}
}

const runHelperEnv = "QUIETBOX_RUN_HELPER_CONFIG"

// TestLockRunHelperProcess is not a test: TestLockKilledRunLeavesNoStrandedLock
// re-executes the test binary with runHelperEnv set, and this function then
// runs `quietbox run` on that config, printing its progress to stdout, until
// it is killed.
func TestLockRunHelperProcess(t *testing.T) {
	cfg := os.Getenv(runHelperEnv)
	if cfg == "" {
		t.Skip("helper process only")
	}
	root := newRootCmd()
	root.SetOut(os.Stdout)
	root.SetErr(os.Stdout)
	root.SetArgs([]string{"run", "--config", cfg})
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintf(os.Stdout, "run: %v\n", err)
		os.Exit(1)
	}
}

// startRunProcess runs `quietbox run` in a child process and returns its
// output and the child. The child is killed at cleanup whatever happens.
func (e *m3Env) startRunProcess() (*syncBuffer, *exec.Cmd) {
	e.t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockRunHelperProcess$")
	cmd.Env = append(os.Environ(), runHelperEnv+"="+e.cfg)
	// A writer, not a pipe: exec copies the output itself and cmd.Wait
	// returns only once it is all in out, so Wait is never racing a reader.
	out := &syncBuffer{}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		if cmd.ProcessState == nil { // not reaped by kill9: kill and reap it here, once
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return out, cmd
}

func kill9(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("child did not die of SIGKILL: %v (%v)", err, cmd.ProcessState)
	}
}

func TestLockKilledRunLeavesNoStrandedLock(t *testing.T) {
	e := newM3Env(t)
	daemonLock, passLock := e.db+".lock.run", e.db+".lock"

	// 1. Killed while idle, waiting for mail.
	out, child := e.startRunProcess()
	if !waitFor(t, 30*time.Second, func() bool { return strings.Contains(out.String(), "pass done") }) {
		t.Fatalf("child run did not finish its first pass\n%s", out.String())
	}
	if _, err := e.runCtx(10*time.Second, "run"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second run beside the child: err = %v", err)
	}
	kill9(t, child)
	t.Logf("child run, killed with SIGKILL while idle:\n%s", out.String())
	// An idle run holds only its daemon lock, so this case is about that lock:
	// a new run starts at once. (The pass lock is free after a kill too; that
	// is proved by the unit test TestLocksAreNotStrandedBySIGKILL, which kills
	// a child holding both locks.)
	newOut, stopNew := e.startRun() // fails the test unless its first pass completes
	stopNew()
	if strings.Contains(newOut.String(), "already running") {
		t.Fatalf("a new run after the kill was refused:\n%s", newOut.String())
	}

	e.deliverBatch()
	start := time.Now()
	o, err := e.runCtx(10*time.Second, "once", "--lock-timeout", "1s")
	took := time.Since(start)
	t.Logf("once right after the kill (%v):\n%s", took.Round(10*time.Millisecond), o)
	if err != nil || strings.Contains(o, "waiting for another quietbox") {
		t.Fatalf("once after kill -9: %v\n%s", err, o)
	}
	e.assertFiledAsExpected()
	for _, p := range []string{daemonLock, passLock} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("lock file %s: %v (it is expected to stay behind, empty and harmless)", p, err)
		}
	}

	// 2. Killed while waiting for the pass lock (holding only its daemon lock):
	// a new run starts at once afterwards.
	held, err := engine.AcquireLock(passLock)
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(held) }
	t.Cleanup(release)
	out, child = e.startRunProcess()
	if !waitFor(t, 30*time.Second, func() bool { return strings.Contains(out.String(), "waiting for another quietbox") }) {
		t.Fatalf("child run did not wait for the held pass lock\n%s", out.String())
	}
	if !strings.Contains(out.String(), "waiting for another quietbox... (lock file "+passLock+")") {
		t.Errorf("the wait notice does not name the lock file:\n%s", out.String())
	}
	kill9(t, child)
	release()
	t.Logf("child run, killed with SIGKILL while waiting for the pass lock:\n%s", out.String())
	runOut, stop := e.startRun() // fails the test unless its first pass completes
	stop()
	if strings.Contains(runOut.String(), "waiting for another quietbox") {
		t.Errorf("a new run had to wait after the kill:\n%s", runOut.String())
	}
}
