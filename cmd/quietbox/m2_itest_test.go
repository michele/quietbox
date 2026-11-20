//go:build integration

package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/imapbackend"
	"github.com/michele/quietbox/internal/store"
)

// newM2Env writes a config with exactly one folder per type, all of which exist
// on the seeded server, so every detector has a target and init creates nothing.
func newM2Env(t *testing.T) *learnEnv {
	t.Helper()
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "test")
	dir := t.TempDir()
	e := &learnEnv{
		t:   t,
		cfg: filepath.Join(dir, "config.toml"),
		db:  filepath.Join(dir, "state", "q.db"),
		csv: filepath.Join(dir, "report.csv"),
	}
	cfg := fmt.Sprintf(`
[imap]
host = "127.0.0.1"
port = 1143
tls = "none"
username = "me@quietbox.test"

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
[[folder]]
name = "SaneBlackHole"
type = "blackhole"
`, e.db)
	if err := os.WriteFile(e.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// folderState is what the server says about one folder.
type folderState struct{ Messages, Unseen uint32 }

// mailboxState reads message and unseen counts of every seeded folder over a
// single connection that is logged out cleanly (Dovecot allows only a few
// connections per user, and the test packages run in parallel).
func mailboxState(t *testing.T) map[string]folderState {
	t.Helper()
	c, err := imapclient.DialInsecure("127.0.0.1:1143", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()
	if err := c.Login("me@quietbox.test", "test").Wait(); err != nil {
		t.Fatal(err)
	}
	out := map[string]folderState{}
	for _, f := range seededFolders {
		st, err := c.Status(f, &imap.StatusOptions{NumMessages: true, NumUnseen: true}).Wait()
		if err != nil {
			t.Fatalf("status %s: %v", f, err)
		}
		out[f] = folderState{*st.NumMessages, *st.NumUnseen}
	}
	return out
}

func assertMailboxUnchanged(t *testing.T, before, after map[string]folderState) {
	t.Helper()
	for f, b := range before {
		if after[f] != b {
			t.Errorf("%s changed on the server: %+v -> %+v", f, b, after[f])
		}
	}
}

// seededSubjects are subjects of the M2 fixtures; none may ever reach a report.
var seededSubjects = []string{"Lunch tomorrow", "Quote request", "Weekly Digest", "Ricevuta del tuo ordine", "20% off", "Letter #12", "A quiet note", "has shipped", "Weekend deals"}

func assertNoSubjects(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range seededSubjects {
		if strings.Contains(text, s) {
			t.Errorf("%s leaks the subject %q", what, s)
		}
	}
}

// Expected verdicts for the new-sender mail the M2 fixtures plant after the
// 2026-05-20 cutoff. Derived from the fixture headers and the rules, not from
// running the code:
//
//	dana      plain human                                  -> stays (no rule)
//	vendor    reply to mail sent 05-05, bulk-looking       -> stays (thread beats detectors)
//	editor    List-Id + List-Unsubscribe, left in INBOX    -> SaneNews (newsletter): a "hidden important" miss
//	pizzeria  noreply@ + SES header + "Ricevuta", in INBOX -> SaneBulk (bulk)
//	newbrand  Precedence bulk + X-Campaign-Id, in INBOX    -> SaneLater (later)
//	freshpress  List-Id + List-Unsubscribe, in SaneNews    -> SaneNews (newsletter)
//	indie     plain human-looking, filed in SaneNews       -> stays: a miss
//	shoplab   noreply@ + SES header + "shipped", SaneBulk  -> SaneBulk (bulk)
//	deals     Precedence bulk + X-Campaign-Id, SaneLater   -> SaneLater (later)
var m2Verdicts = []struct {
	from, actual, predicted string
	rule                    classify.Rule
}{
	{"dana@colleagues.test", "INBOX", "INBOX", classify.RuleNone},
	{"vendor@supplier.test", "INBOX", "INBOX", classify.RuleThread},
	{"editor@weekly-digest.test", "INBOX", "SaneNews", classify.RuleNewsletter},
	{"noreply@pizzeria.test", "INBOX", "SaneBulk", classify.RuleBulk},
	{"offers@newbrand.test", "INBOX", "SaneLater", classify.RuleLater},
	{"letters@freshpress.test", "SaneNews", "SaneNews", classify.RuleNewsletter},
	{"quiet@indie.test", "SaneNews", "INBOX", classify.RuleNone},
	{"noreply@shoplab.test", "SaneBulk", "SaneBulk", classify.RuleBulk},
	{"promo@deals.test", "SaneLater", "SaneLater", classify.RuleLater},
}

func TestEvalEngineAgainstTestServer(t *testing.T) {
	e := newM2Env(t)
	cfg, err := config.Load(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := imapbackend.Dial(context.Background(), imapbackend.Options{
		Host: cfg.IMAP.Host, Port: cfg.IMAP.Port, TLS: cfg.IMAP.TLS, Username: cfg.IMAP.Username, Password: cfg.IMAP.Password,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	rep, err := engine.Eval(context.Background(), cfg, b, engine.EvalOptions{Cutoff: time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	byFrom := map[string]engine.EvalRow{}
	for _, r := range rep.Rows {
		byFrom[r.From] = r
	}
	for _, v := range m2Verdicts {
		r, ok := byFrom[v.from]
		if !ok {
			t.Errorf("%s: not in the test set", v.from)
			continue
		}
		if !r.NewSender || r.Actual != v.actual || r.Decision.Folder != v.predicted || r.Decision.Rule != v.rule {
			t.Errorf("%s: new=%v actual=%s predicted=%s via %s (%s); want new=true actual=%s predicted=%s via %s",
				v.from, r.NewSender, r.Actual, r.Decision.Folder, r.Decision.Rule, r.Decision.Reason, v.actual, v.predicted, v.rule)
		}
	}

	// INBOX holds 9 test messages (carol, zoe, alice, fresh@substack and the five
	// planted ones). Four would be hidden: fresh@substack (domain default) plus
	// editor, pizzeria and newbrand, all from new senders.
	if rep.InboxTotal != 9 || rep.HiddenImportant != 4 || rep.HiddenImportantNew != 4 {
		t.Errorf("hidden important = %d of %d (new %d), want 4 of 9 (new 4)", rep.HiddenImportant, rep.InboxTotal, rep.HiddenImportantNew)
	}
	// Known senders agree by construction: alice (INBOX) and nobody else.
	if rep.Known.Total != 1 || rep.Known.Agree != 1 {
		t.Errorf("known = %+v, want 1 of 1", rep.Known)
	}
}

func TestEvalCommandAgainstTestServer(t *testing.T) {
	e := newM2Env(t)
	before := mailboxState(t)

	out, _, err := e.run("", "eval", "--cutoff", "2026-05-20", "--csv", e.csv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nothing was changed", "agreement, new senders", "Hidden important: 4 of 9", "newsletter", "bulk", "later", "thread"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	assertNoSubjects(t, "eval output", out)

	raw, err := os.ReadFile(e.csv)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSubjects(t, "eval csv", string(raw))
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	misses := map[string]string{} // from -> predicted
	for _, r := range rows[1:] {
		if r[2] == r[3] {
			t.Errorf("agreeing row in the disagreement csv: %v", r)
		}
		misses[r[1]] = r[3]
	}
	for from, predicted := range map[string]string{
		"editor@weekly-digest.test": "SaneNews", "noreply@pizzeria.test": "SaneBulk",
		"offers@newbrand.test": "SaneLater", "quiet@indie.test": "INBOX",
	} {
		if misses[from] != predicted {
			t.Errorf("csv: %s predicted %q, want %q", from, misses[from], predicted)
		}
	}
	for _, agree := range []string{"dana@colleagues.test", "vendor@supplier.test", "letters@freshpress.test", "noreply@shoplab.test", "promo@deals.test"} {
		if _, bad := misses[agree]; bad {
			t.Errorf("%s agrees and must not be in the disagreement csv", agree)
		}
	}
	if fi, err := os.Stat(e.csv); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("csv mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}

	// Read-only: no database, no folders, no flags.
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Error("eval must not create the database")
	}
	assertMailboxUnchanged(t, before, mailboxState(t))
}

func TestScanCommandAgainstTestServer(t *testing.T) {
	e := newM2Env(t)

	// Before init there is nothing to scan against.
	if _, _, err := e.run("", "scan", "--now", "2026-06-01"); err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("scan before init: err = %v", err)
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Fatal("scan must not create the database")
	}

	if _, _, err := e.run("", "init", "--now", "2026-06-01", "--report", ""); err != nil {
		t.Fatal(err)
	}
	st := e.openStore()
	auditBefore, _ := st.Audit(context.Background())
	sendersBefore, _ := st.Senders(context.Background(), store.SenderFilter{})
	before := mailboxState(t)

	out, _, err := e.run("", "scan", "--now", "2026-06-01", "--all", "--csv", e.csv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"nothing was changed", "would move", "By folder:", "By rule:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// promos@travel.test is filed in SaneLater most of the time (it also has an
	// INBOX copy): its learned route moves the INBOX copy there.
	if !strings.Contains(out, "promos@travel.test") || !strings.Contains(out, "SaneLater") {
		t.Errorf("expected the learned travel promo route in:\n%s", out)
	}
	// After init every INBOX sender is known, so the new INBOX mail is classified
	// by its learned INBOX route, not by detectors.
	if !strings.Contains(out, "dana@colleagues.test") {
		t.Errorf("--all should list INBOX mail that stays:\n%s", out)
	}
	assertNoSubjects(t, "scan output", out)
	raw, err := os.ReadFile(e.csv)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSubjects(t, "scan csv", string(raw))
	if fi, err := os.Stat(e.csv); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("csv mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}

	// Nothing changed anywhere.
	auditAfter, _ := st.Audit(context.Background())
	sendersAfter, _ := st.Senders(context.Background(), store.SenderFilter{})
	if len(auditAfter) != len(auditBefore) || len(sendersAfter) != len(sendersBefore) {
		t.Error("scan wrote to the database")
	}
	assertMailboxUnchanged(t, before, mailboxState(t))
}
