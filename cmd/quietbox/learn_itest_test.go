//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/michele/quietbox/internal/store"
)

type learnEnv struct {
	t      *testing.T
	cfg    string // config path
	db     string // sqlite path
	csv    string // report path
	folder string // extra configured folder that does not exist yet
}

// newLearnEnv writes a config with its own state directory and its own uniquely
// named, not-yet-existing folder, so these tests never disturb the `check`
// golden test or the real home directory.
func newLearnEnv(t *testing.T) *learnEnv {
	t.Helper()
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "test")
	dir := t.TempDir()
	e := &learnEnv{
		t:      t,
		cfg:    filepath.Join(dir, "config.toml"),
		db:     filepath.Join(dir, "state", "q.db"),
		csv:    filepath.Join(dir, "learn-report.csv"),
		folder: fmt.Sprintf("QBLearn-%d", time.Now().UnixNano()),
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
[[folder]]
name = %q
type = "later"
`, e.db, e.folder)
	if err := os.WriteFile(e.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *learnEnv) run(stdin string, args ...string) (stdout, stderr string, err error) {
	e.t.Helper()
	var out, errb bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errb)
	if stdin != "" {
		root.SetIn(strings.NewReader(stdin))
	}
	root.SetArgs(append([]string{"--config", e.cfg}, args...))
	err = root.Execute()
	return out.String(), errb.String(), err
}

func (e *learnEnv) initArgs(extra ...string) []string {
	return append([]string{"init", "--now", "2026-06-01", "--report", e.csv}, extra...)
}

func (e *learnEnv) openStore() *store.Store {
	e.t.Helper()
	st, err := store.OpenExisting(e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = st.Close() })
	return st
}

// serverState reads folder existence and UNSEEN counts straight from the server.
func serverState(t *testing.T, folders ...string) map[string]uint32 {
	t.Helper()
	c, err := imapclient.DialInsecure("127.0.0.1:1143", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Logout().Wait(); _ = c.Close() }()
	if err := c.Login("me@quietbox.test", "test").Wait(); err != nil {
		t.Fatal(err)
	}
	out := map[string]uint32{}
	for _, f := range folders {
		st, err := c.Status(f, &imap.StatusOptions{NumUnseen: true}).Wait()
		if err != nil {
			continue // folder does not exist
		}
		out[f] = *st.NumUnseen
	}
	return out
}

var seededFolders = []string{"INBOX", "Sent", "SaneLater", "SaneNews", "SaneBulk", "SaneBlackHole"}

func routes(t *testing.T, st *store.Store) map[string]store.SenderRow {
	t.Helper()
	rows, err := st.Senders(context.Background(), store.SenderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]store.SenderRow{}
	for _, r := range rows {
		m[r.Address] = r
	}
	return m
}

func TestLearnDryRunChangesNothing(t *testing.T) {
	e := newLearnEnv(t)
	before := serverState(t, append(seededFolders, e.folder)...)
	out, _, err := e.run("", e.initArgs("--dry-run")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Would create folders: "+e.folder) || !strings.Contains(out, "nothing was changed") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Errorf("dry run must not create the database (stat err = %v)", err)
	}
	if _, err := os.Stat(e.csv); err != nil {
		t.Errorf("dry run still writes the report: %v", err)
	}
	after := serverState(t, append(seededFolders, e.folder)...)
	if _, exists := after[e.folder]; exists {
		t.Error("dry run created a folder on the server")
	}
	for f, n := range before {
		if after[f] != n {
			t.Errorf("%s UNSEEN %d -> %d", f, n, after[f])
		}
	}
}

func TestLearnDeclinedFolderCreation(t *testing.T) {
	e := newLearnEnv(t)
	out, _, err := e.run("n\n", e.initArgs()...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Not created") {
		t.Errorf("output:\n%s", out)
	}
	if _, exists := serverState(t, e.folder)[e.folder]; exists {
		t.Error("folder was created despite answering no")
	}
	audit, err := e.openStore().Audit(context.Background())
	if err != nil || len(audit) != 0 {
		t.Errorf("declining must leave no audit rows: %v %v", audit, err)
	}
}

func TestLearnEndToEnd(t *testing.T) {
	e := newLearnEnv(t)
	before := serverState(t, seededFolders...)

	out, _, err := e.run("", e.initArgs("--yes")...)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Created folders: "+e.folder) {
		t.Errorf("output:\n%s", out)
	}
	if _, exists := serverState(t, e.folder)[e.folder]; !exists {
		t.Fatal("folder was not created on the server")
	}

	st := e.openStore()
	audit, err := st.Audit(context.Background())
	if err != nil || len(audit) != 1 || audit[0].Action != "create_folder" || audit[0].ToFolder != e.folder || audit[0].Undone {
		t.Errorf("audit = %+v, %v", audit, err)
	}

	got := routes(t, st)
	want := map[string]struct {
		folder string
		mixed  bool
	}{
		"alice@friends.test":       {"INBOX", false},
		"bob@work.test":            {"INBOX", false},
		"carol@unknown.test":       {"INBOX", false}, // no Message-ID header
		"zoe@intl.test":            {"INBOX", false}, // RFC 2047 name and subject
		"dave@old.test":            {"INBOX", false}, // only ever written to
		"fresh@substack.test":      {"INBOX", false},
		"eve@club.test":            {"SaneNews", false}, // written to, but 4 news messages
		"writer1@substack.test":    {"SaneNews", false},
		"writer2@substack.test":    {"SaneNews", false},
		"writer3@substack.test":    {"SaneNews", false},
		"writer4@substack.test":    {"SaneNews", false},
		"digest@buttondown.test":   {"SaneNews", false},
		"campaign@mcsv.test":       {"SaneNews", false},
		"drift@mixed.test":         {"SaneNews", true}, // recency beats the older Later mail
		"store@bigstore.test":      {"SaneBulk", true},
		"promos@travel.test":       {"SaneLater", true}, // also has an INBOX copy
		"noreply@shop.test":        {"SaneBulk", false},
		"notifications@forge.test": {"SaneBulk", false},
		"ricevute@negozio.test":    {"SaneBulk", false},
		"spam@pushy.test":          {"SaneBlackHole", false},
		// M2 fixtures: one message each, so never mixed.
		"dana@colleagues.test":      {"INBOX", false},
		"vendor@supplier.test":      {"INBOX", false},
		"editor@weekly-digest.test": {"INBOX", false},
		"noreply@pizzeria.test":     {"INBOX", false},
		"offers@newbrand.test":      {"INBOX", false},
		"quotes@supplier.test":      {"INBOX", false}, // only ever written to
		"letters@freshpress.test":   {"SaneNews", false},
		"quiet@indie.test":          {"SaneNews", false},
		"noreply@shoplab.test":      {"SaneBulk", false},
		"promo@deals.test":          {"SaneLater", false},
	}
	if len(got) != len(want) {
		t.Errorf("learned %d senders, want %d", len(got), len(want))
	}
	for addr, w := range want {
		if r, ok := got[addr]; !ok || r.RouteFolder != w.folder || r.Mixed != w.mixed || r.RouteSource != store.SourceLearned {
			t.Errorf("%s: got %+v (present=%v), want %+v", addr, r, ok, w)
		}
	}
	for _, absent := range []string{"oldnews@letters.test", "me@quietbox.test"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s must not be learned (older than the lookback / is me)", absent)
		}
	}
	if got["alice@friends.test"].SentTo != 1 || got["eve@club.test"].SentTo != 1 {
		t.Errorf("sent_to not recorded: %+v %+v", got["alice@friends.test"], got["eve@club.test"])
	}
	domains, _ := st.Domains(context.Background())
	if len(domains) != 1 || domains[0].Domain != "substack.test" || domains[0].Folder != "SaneNews" ||
		domains[0].Senders != 5 || domains[0].Agreeing != 4 {
		t.Errorf("domains = %+v", domains)
	}

	// The CSV matches the reviewed golden file (the unique folder name is normalised).
	raw, err := os.ReadFile(e.csv)
	if err != nil {
		t.Fatal(err)
	}
	gotCSV := strings.ReplaceAll(string(raw), e.folder, "QBLearn")
	golden := "../../testdata/golden/learn-report.csv"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(gotCSV), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wantCSV, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if gotCSV != string(wantCSV) {
		t.Errorf("learn-report.csv differs from golden.\n--- got ---\n%s\n--- want ---\n%s", gotCSV, wantCSV)
	}
	if fi, err := os.Stat(e.csv); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("report should be private (0600): %v %v", fi, err)
	}

	// Reading must not mark anything as read.
	after := serverState(t, seededFolders...)
	for f, n := range before {
		if after[f] != n {
			t.Errorf("%s UNSEEN changed %d -> %d: scanning marked mail as read", f, n, after[f])
		}
	}

	// A second run creates nothing and changes nothing.
	obs1, _ := st.Observations(context.Background())
	out2, _, err := e.run("", e.initArgs("--yes")...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2, "Created folders") {
		t.Errorf("second run created folders:\n%s", out2)
	}
	got2 := routes(t, e.openStore())
	obs2, _ := e.openStore().Observations(context.Background())
	if len(got2) != len(got) || len(obs1) != len(obs2) {
		t.Errorf("second run changed state: senders %d->%d, observations %d->%d", len(got), len(got2), len(obs1), len(obs2))
	}
	for addr, r := range got {
		if r2 := got2[addr]; r2.RouteFolder != r.RouteFolder || r2.Mixed != r.Mixed {
			t.Errorf("%s changed: %+v -> %+v", addr, r, r2)
		}
	}
	if a, _ := e.openStore().Audit(context.Background()); len(a) != 1 {
		t.Errorf("second run must not add audit rows, have %d", len(a))
	}

	// Re-learning one folder keeps everything else.
	if _, _, err := e.run("", e.initArgs("--folder", "SaneNews")...); err != nil {
		t.Fatal(err)
	}
	if got3 := routes(t, e.openStore()); len(got3) != len(got) {
		t.Errorf("--folder SaneNews changed the sender count: %d -> %d", len(got), len(got3))
	}
	if _, _, err := e.run("", e.initArgs("--folder", "Nope")...); err == nil {
		t.Error("--folder with an unknown folder must fail")
	}

	// senders reads the database only, so no password is needed.
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "")
	mixed, _, err := e.run("", "senders", "--mixed")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"drift@mixed.test", "store@bigstore.test", "promos@travel.test"} {
		if !strings.Contains(mixed, a) {
			t.Errorf("senders --mixed missing %s:\n%s", a, mixed)
		}
	}
	if !strings.Contains(mixed, "3 senders") {
		t.Errorf("senders --mixed:\n%s", mixed)
	}
	news, _, err := e.run("", "senders", "--folder", "SaneNews")
	if err != nil || !strings.Contains(news, "10 senders") || strings.Contains(news, "alice@") {
		t.Errorf("senders --folder SaneNews (err %v):\n%s", err, news)
	}
}

func TestSendersBeforeInitSaysSo(t *testing.T) {
	e := newLearnEnv(t)
	_, _, err := e.run("", "senders")
	if err == nil || !strings.Contains(err.Error(), "init") {
		t.Errorf("err = %v", err)
	}
	if _, statErr := os.Stat(e.db); !os.IsNotExist(statErr) {
		t.Error("senders must not create the database")
	}
}

// initNoReportFlag runs init without --report, from an empty working directory
// that must stay empty.
func (e *learnEnv) initNoReportFlag(extra ...string) (stdout, stderr string, err error) {
	e.t.Helper()
	cwd := e.t.TempDir()
	e.t.Chdir(cwd)
	e.t.Cleanup(func() { assertOnlyEntries(e.t, cwd) })
	return e.run("", append([]string{"init", "--now", "2026-06-01"}, extra...)...)
}

func (e *learnEnv) defaultReport() string {
	return filepath.Join(filepath.Dir(e.db), "q.learn-report.csv")
}

func TestLearnDefaultReportNextToDatabase(t *testing.T) {
	e := newLearnEnv(t)
	out, _, err := e.initNoReportFlag("--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\nReport written to "+e.defaultReport()+"\n") {
		t.Errorf("output:\n%s", out)
	}
	fi, err := os.Stat(e.defaultReport())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("report next to the database: %v %v (want mode 0600)", fi, err)
	}
	raw, _ := os.ReadFile(e.defaultReport())
	if !strings.Contains(string(raw), "alice@friends.test") {
		t.Errorf("report content:\n%s", raw)
	}
}

func TestLearnDryRunDefaultReportCreatesStateDir(t *testing.T) {
	e := newLearnEnv(t)
	stateDir := filepath.Dir(e.db)
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("state dir should not exist yet: %v", err)
	}
	out, _, err := e.initNoReportFlag("--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Report written to "+e.defaultReport()) {
		t.Errorf("output:\n%s", out)
	}
	if fi, err := os.Stat(stateDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir: %v %v (want mode 0700)", fi, err)
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Errorf("dry run must not create the database (stat err = %v)", err)
	}
	assertOnlyEntries(t, stateDir, "q.learn-report.csv")
}

func TestLearnEmptyReportFlagWritesNothing(t *testing.T) {
	e := newLearnEnv(t)
	out, _, err := e.initNoReportFlag("--yes", "--report", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Report written") {
		t.Errorf("output:\n%s", out)
	}
	ents, err := os.ReadDir(filepath.Dir(e.db))
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		if strings.HasSuffix(ent.Name(), ".csv") {
			t.Errorf("--report '' wrote %s", ent.Name())
		}
	}
}

func TestLearnUnwritableReportIsAWarning(t *testing.T) {
	e := newLearnEnv(t)
	bad := t.TempDir() // a directory: cannot be written as a file
	out, errOut, err := e.initNoReportFlag("--yes", "--report", bad)
	if err != nil {
		t.Fatalf("a report failure must not fail init: %v", err)
	}
	if !strings.Contains(errOut, "warning: could not write the report to "+bad+": ") {
		t.Errorf("stderr:\n%s", errOut)
	}
	if strings.Contains(out, "Report written") {
		t.Errorf("stdout claims the report was written:\n%s", out)
	}
	if got := routes(t, e.openStore()); len(got) == 0 {
		t.Error("the learned state was not saved")
	}
	assertOnlyEntries(t, bad)
}
