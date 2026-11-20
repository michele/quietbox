package engine

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
	"github.com/michele/quietbox/internal/store"
)

var learnNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func daysAgo(d int) time.Time { return learnNow.AddDate(0, 0, -d) }

func learnConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	src := `
[imap]
host = "127.0.0.1"
port = 1143
tls = "none"
username = "me@example.test"
` + extra + `
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
`
	cfg, err := config.Parse([]byte(src), func(string) string { return "pw" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func hdr(from string, days int, mid string, to ...string) mail.Header {
	return mail.Header{From: from, To: to, Date: daysAgo(days), InternalDate: daysAgo(days), MessageID: mid, Subject: "s"}
}

// miniMailbox: alice+bob in INBOX, a substack domain, a mixed sender, and Sent.
func miniMailbox() *mailtest.Fake {
	return &mailtest.Fake{
		Caps: mail.Capabilities{"IMAP4REV1"},
		Folders: []mail.Folder{
			{Name: "INBOX"}, {Name: "SaneLater"}, {Name: "SaneNews"}, {Name: "SaneBulk"}, {Name: "SaneBlackHole"},
			{Name: "Sent", Attrs: []string{`\Sent`}},
		},
		Messages: map[string][]mail.Header{
			"INBOX": {
				hdr("alice@friends.test", 5, "<a1>"),
				hdr("alice@friends.test", 9, "<a2>"),
				hdr("fresh@substack.test", 3, "<f1>"),
				hdr("old@letters.test", 300, "<o1>"), // older than the 6mo lookback
				hdr("me@example.test", 2, "<self>"),  // from me: skipped
				{InternalDate: daysAgo(4), MessageID: "<nofrom>"},
			},
			"SaneNews": {
				hdr("w1@substack.test", 10, "<n1>"), hdr("w2@substack.test", 11, "<n2>"),
				hdr("w3@substack.test", 12, "<n3>"), hdr("w4@substack.test", 13, "<n4>"),
				hdr("eve@club.test", 20, "<e1>"), hdr("eve@club.test", 30, "<e2>"), hdr("eve@club.test", 40, "<e3>"),
				hdr("w1@substack.test", 10, "<n1>"), // duplicate in the same folder: counts once
			},
			"SaneBulk":      {hdr("store@big.test", 56, "<b1>"), hdr("store@big.test", 58, "<b2>"), hdr("store@big.test", 60, "<b3>")},
			"SaneLater":     {hdr("store@big.test", 46, "<l1>"), hdr("store@big.test", 49, "<l2>")},
			"SaneBlackHole": {hdr("spam@pushy.test", 7, "<s1>")},
			"Sent": {
				hdr("me@example.test", 100, "<sent1>", "alice@friends.test", "Dave@Old.test"),
				hdr("me@example.test", 90, "<sent2>", "eve@club.test", "me@example.test"),
			},
		},
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func routeOf(r *LearnReport, addr string) (folder string, mixed bool, ok bool) {
	for _, s := range r.Senders {
		if s.Address == addr {
			return s.Folder, s.Mixed, true
		}
	}
	return "", false, false
}

func TestLearnFull(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	r, err := Learn(context.Background(), learnConfig(t, ""), fake, st, LearnOptions{Now: learnNow})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		folder string
		mixed  bool
	}{
		"alice@friends.test":  {"INBOX", false},
		"fresh@substack.test": {"INBOX", false},
		"w1@substack.test":    {"SaneNews", false},
		"eve@club.test":       {"SaneNews", false}, // sent to, but 3 news messages at 100%
		"dave@old.test":       {"INBOX", false},    // sent to only, address lower-cased
		"store@big.test":      {"SaneBulk", true},
		"spam@pushy.test":     {"SaneBlackHole", false},
	}
	for addr, w := range want {
		f, m, ok := routeOf(r, addr)
		if !ok || f != w.folder || m != w.mixed {
			t.Errorf("%s: got route=%q mixed=%v ok=%v, want %+v", addr, f, m, ok, w)
		}
	}
	for _, addr := range []string{"old@letters.test", "me@example.test"} {
		if _, _, ok := routeOf(r, addr); ok {
			t.Errorf("%s must not be learned", addr)
		}
	}
	if r.SkippedNoFrom != 1 || r.SkippedSelf != 1 {
		t.Errorf("skipped nofrom=%d self=%d", r.SkippedNoFrom, r.SkippedSelf)
	}
	if got := r.Stats["w1@substack.test"].Counts["SaneNews"]; got != 1 {
		t.Errorf("duplicate in one folder must count once, got %d", got)
	}
	if got := r.Stats["store@big.test"].Counts; got["SaneBulk"] != 3 || got["SaneLater"] != 2 {
		t.Errorf("copies across folders count separately: %v", got)
	}
	if len(r.Domains) != 1 || r.Domains[0].Domain != "substack.test" || r.Domains[0].Folder != "SaneNews" ||
		r.Domains[0].Senders != 5 || r.Domains[0].Agreeing != 4 {
		t.Errorf("domains = %+v", r.Domains)
	}
	// Persisted.
	rows, err := st.Senders(context.Background(), store.SenderFilter{})
	if err != nil || len(rows) != len(r.Senders) {
		t.Fatalf("stored %d senders (err %v), report has %d", len(rows), err, len(r.Senders))
	}
	if d, _ := st.Domains(context.Background()); len(d) != 1 {
		t.Errorf("stored domains = %v", d)
	}
	if f, _ := st.Folders(context.Background()); len(f) != 5 {
		t.Errorf("stored folders = %v", f)
	}
	if len(fake.Created) != 0 {
		t.Errorf("nothing was missing, but created %v", fake.Created)
	}
}

func TestLearnIsIdempotent(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	cfg := learnConfig(t, "")
	for i := 0; i < 2; i++ {
		if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow}); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := st.Senders(context.Background(), store.SenderFilter{})
	obs, _ := st.Observations(context.Background())
	if len(rows) != 10 {
		t.Errorf("senders after two runs = %d, want 10", len(rows))
	}
	if len(obs) != 16 {
		t.Errorf("observations after two runs = %d (duplicated?)", len(obs))
	}
}

func TestLearnCreatesMissingFoldersAuditedFirst(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	fake.Folders = fake.Folders[:3] // drop SaneBulk and SaneBlackHole (+Sent below)
	fake.Folders = append(fake.Folders, mail.Folder{Name: "Sent", Attrs: []string{`\Sent`}})
	var auditedBeforeCreate []bool
	fake.OnCreate = func(name string) {
		rows, _ := st.Audit(context.Background())
		ok := false
		for _, a := range rows {
			ok = ok || (a.Action == "create_folder" && a.ToFolder == name)
		}
		auditedBeforeCreate = append(auditedBeforeCreate, ok)
	}
	r, err := Learn(context.Background(), learnConfig(t, ""), fake, st, LearnOptions{Now: learnNow, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Created, ",") != "SaneBulk,SaneBlackHole" || len(fake.Created) != 2 {
		t.Errorf("created = %v / %v", r.Created, fake.Created)
	}
	if len(auditedBeforeCreate) != 2 || !auditedBeforeCreate[0] || !auditedBeforeCreate[1] {
		t.Errorf("audit row must exist before each CREATE: %v", auditedBeforeCreate)
	}
}

func TestLearnWithoutConsentCreatesNothing(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	fake.Folders = []mail.Folder{{Name: "INBOX"}, {Name: "Sent", Attrs: []string{`\Sent`}}}
	r, err := Learn(context.Background(), learnConfig(t, ""), fake, st, LearnOptions{Now: learnNow}) // no Yes, no Confirm
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Created) != 0 || len(r.SkippedCreate) != 4 {
		t.Errorf("created=%v skipped=%v", fake.Created, r.SkippedCreate)
	}
	if a, _ := st.Audit(context.Background()); len(a) != 0 {
		t.Errorf("no consent means no audit rows: %v", a)
	}
}

func TestLearnConfirmCallbackPerFolder(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	fake.Folders = []mail.Folder{{Name: "INBOX"}, {Name: "Sent", Attrs: []string{`\Sent`}}}
	var asked []string
	_, err := Learn(context.Background(), learnConfig(t, ""), fake, st, LearnOptions{
		Now: learnNow,
		Confirm: func(name string) (bool, error) {
			asked = append(asked, name)
			return name == "SaneNews", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 4 || len(fake.Created) != 1 || fake.Created[0] != "SaneNews" {
		t.Errorf("asked=%v created=%v", asked, fake.Created)
	}
}

func TestLearnDryRunChangesNothing(t *testing.T) {
	cfg := learnConfig(t, "")
	t.Run("without a store", func(t *testing.T) {
		fake := miniMailbox()
		fake.Folders = fake.Folders[:3]
		fake.Folders = append(fake.Folders, mail.Folder{Name: "Sent", Attrs: []string{`\Sent`}})
		r, err := Learn(context.Background(), cfg, fake, nil, LearnOptions{Now: learnNow, DryRun: true, Yes: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(fake.Created) != 0 || strings.Join(r.WouldCreate, ",") != "SaneBulk,SaneBlackHole" || len(r.Senders) == 0 {
			t.Errorf("created=%v would=%v senders=%d", fake.Created, r.WouldCreate, len(r.Senders))
		}
	})
	t.Run("with a store", func(t *testing.T) {
		fake, st := miniMailbox(), openStore(t)
		if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow, DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if rows, _ := st.Senders(context.Background(), store.SenderFilter{}); len(rows) != 0 {
			t.Errorf("dry-run wrote senders: %v", rows)
		}
		if a, _ := st.Audit(context.Background()); len(a) != 0 {
			t.Errorf("dry-run wrote audit rows: %v", a)
		}
	})
}

func TestLearnSinceLimitsDepth(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	r, err := Learn(context.Background(), learnConfig(t, ""), fake, st, LearnOptions{Now: learnNow, Since: 25 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Only the eve@ message 20 days old is left, the 30/40-day ones fall outside 25d.
	if got := r.Stats["eve@club.test"].Counts["SaneNews"]; got != 1 {
		t.Errorf("eve news count = %d, want 1", got)
	}
	// bigstore mail (46-60 days) is gone entirely.
	if _, _, ok := routeOf(r, "store@big.test"); ok {
		t.Error("store@big.test should be outside --since 25d")
	}
	// Sent history is also limited by --since: dave (100 days ago) is not seen.
	if _, _, ok := routeOf(r, "dave@old.test"); ok {
		t.Error("dave was last written to 100 days ago")
	}
}

func TestLearnOneFolderKeepsTheRest(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	cfg := learnConfig(t, "")
	if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow}); err != nil {
		t.Fatal(err)
	}
	// SaneNews now holds only one new sender; everything else must survive.
	fake.Messages["SaneNews"] = []mail.Header{hdr("brand@new.test", 1, "<x1>")}
	r, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow, Folder: "SaneNews"})
	if err != nil {
		t.Fatal(err)
	}
	if f, _, ok := routeOf(r, "brand@new.test"); !ok || f != "SaneNews" {
		t.Errorf("new sender not learned: %q %v", f, ok)
	}
	if f, _, _ := routeOf(r, "alice@friends.test"); f != "INBOX" {
		t.Errorf("INBOX sender lost in scoped re-learn: %q", f)
	}
	if f, _, _ := routeOf(r, "store@big.test"); f != "SaneBulk" {
		t.Errorf("bulk sender lost: %q", f)
	}
	if f, _, _ := routeOf(r, "eve@club.test"); f != "INBOX" {
		t.Errorf("eve has lost her news mail, so sent-to routes her to INBOX, got %q", f)
	}
	if _, _, ok := routeOf(r, "w1@substack.test"); ok {
		t.Error("old SaneNews sender should be gone after re-learning SaneNews")
	}
	rows, _ := st.Senders(context.Background(), store.SenderFilter{})
	if len(rows) != len(r.Senders) {
		t.Errorf("store has %d senders, report %d", len(rows), len(r.Senders))
	}
}

func TestLearnOneFolderErrors(t *testing.T) {
	cfg := learnConfig(t, "")
	if _, err := Learn(context.Background(), cfg, miniMailbox(), openStore(t), LearnOptions{Now: learnNow, Folder: "Nope"}); err == nil {
		t.Error("unknown folder should fail")
	}
	if _, err := Learn(context.Background(), cfg, miniMailbox(), nil, LearnOptions{Now: learnNow, DryRun: true, Folder: "SaneNews"}); err == nil {
		t.Error("re-learning one folder needs an existing database")
	}
}

func TestLearnSentFolderResolution(t *testing.T) {
	cfg := learnConfig(t, "")
	fake := miniMailbox()
	fake.Folders[5].Attrs = nil // no \Sent attribute
	if _, err := Learn(context.Background(), cfg, fake, openStore(t), LearnOptions{Now: learnNow}); err == nil ||
		!strings.Contains(err.Error(), "sent_folder") {
		t.Errorf("want an error telling the user to set learn.sent_folder, got %v", err)
	}
	cfg = learnConfig(t, "[learn]\nsent_folder = \"Sent\"\n")
	if _, err := Learn(context.Background(), cfg, fake, openStore(t), LearnOptions{Now: learnNow}); err != nil {
		t.Errorf("explicit sent_folder should work: %v", err)
	}
	cfg = learnConfig(t, "[learn]\nsent_folder = \"Elsewhere\"\n")
	if _, err := Learn(context.Background(), cfg, fake, openStore(t), LearnOptions{Now: learnNow}); err == nil {
		t.Error("sent_folder that does not exist should fail")
	}
}

func TestLearnScanFailureLeavesStoreUntouched(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	cfg := learnConfig(t, "")
	if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Senders(context.Background(), store.SenderFilter{})
	boom := errors.New("connection reset")
	fake.FetchErr = boom
	if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	after, _ := st.Senders(context.Background(), store.SenderFilter{})
	if len(after) != len(before) || len(before) == 0 {
		t.Errorf("store changed after a failed scan: %d -> %d", len(before), len(after))
	}
}

func TestLearnReportOutput(t *testing.T) {
	fake, st := miniMailbox(), openStore(t)
	cfg := learnConfig(t, "")
	r, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow})
	if err != nil {
		t.Fatal(err)
	}
	var sum bytes.Buffer
	if err := r.Write(&sum); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SaneNews", "substack.test", "MIXED", "without a usable From"} {
		if !strings.Contains(sum.String(), want) {
			t.Errorf("summary missing %q:\n%s", want, sum.String())
		}
	}

	var out bytes.Buffer
	if err := r.WriteCSV(&out); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	wantHead := "kind,address_or_domain,route_folder,route_source,mixed,sent_to,INBOX,SaneLater,SaneNews,SaneBulk,SaneBlackHole,last_seen,reason"
	if got := strings.Join(recs[0], ","); got != wantHead {
		t.Errorf("header = %s", got)
	}
	by := map[string][]string{}
	for _, rec := range recs[1:] {
		by[rec[0]+":"+rec[1]] = rec
	}
	big := by["sender:store@big.test"]
	if big == nil || big[2] != "SaneBulk" || big[3] != "learned" || big[4] != "true" ||
		big[7] != "2" || big[9] != "3" || big[11] != "2026-04-16" {
		t.Errorf("store@big.test row = %v", big)
	}
	dom := by["domain:substack.test"]
	if dom == nil || dom[2] != "SaneNews" || dom[3] != "domain" || !strings.Contains(dom[12], "4 of 5") {
		t.Errorf("domain row = %v", dom)
	}
	// Sorted: senders by address, then domains.
	for i := 2; i < len(recs); i++ {
		a, b := recs[i-1], recs[i]
		if a[0] == b[0] && a[1] > b[1] {
			t.Errorf("rows not sorted: %s before %s", a[1], b[1])
		}
	}
}

func TestLearnStoresSentMessageIDs(t *testing.T) {
	st := openStore(t)
	cfg := learnConfig(t, "")
	if _, err := Learn(context.Background(), cfg, miniMailbox(), st, LearnOptions{Now: learnNow}); err != nil {
		t.Fatal(err)
	}
	ids, err := st.SentMessageIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("sent IDs = %v", ids)
	}
	for _, id := range []string{"<sent1>", "<sent2>"} {
		if _, ok := ids[id]; !ok {
			t.Errorf("missing %s", id)
		}
	}

	// Re-learning one folder must not touch the Sent-derived data.
	if _, err := Learn(context.Background(), cfg, miniMailbox(), st, LearnOptions{Now: learnNow, Folder: "SaneNews"}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.SentMessageIDs(context.Background()); len(ids) != 2 {
		t.Errorf("scoped learn changed sent IDs: %v", ids)
	}
}

func TestLearnDryRunStoresNoSentMessageIDs(t *testing.T) {
	st := openStore(t)
	if _, err := Learn(context.Background(), learnConfig(t, ""), miniMailbox(), st, LearnOptions{Now: learnNow, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.SentMessageIDs(context.Background()); len(ids) != 0 {
		t.Errorf("dry run stored %v", ids)
	}
}

func TestLearnRecordsCursorsBeforeScan(t *testing.T) {
	cfg := learnConfig(t, "")
	fake := miniMailbox()
	before, err := fake.FolderStatus(context.Background(), "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	sentBefore, _ := fake.FolderStatus(context.Background(), "Sent")
	var late uint32
	fake.OnFetch = func(folder string) { // mail arriving while the scan runs
		if folder == "INBOX" && late == 0 {
			late = fake.Append("INBOX", hdr("late@x.test", 0, "<late>"))
		}
	}
	st := openStore(t)
	if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow, Yes: true}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := st.Cursor(context.Background(), "INBOX")
	if err != nil || !ok || c.UIDValidity != before.UIDValidity || c.NextUID != before.UIDNext {
		t.Fatalf("INBOX cursor = %+v ok=%v err=%v, want next %d", c, ok, err, before.UIDNext)
	}
	if late < c.NextUID {
		t.Errorf("late message uid %d is below the cursor %d: it would be missed", late, c.NextUID)
	}
	sc, ok, _ := st.Cursor(context.Background(), "Sent")
	if !ok || sc.NextUID != sentBefore.UIDNext {
		t.Errorf("Sent cursor = %+v ok=%v, want next %d", sc, ok, sentBefore.UIDNext)
	}
	if _, ok, _ := st.Cursor(context.Background(), "SaneNews"); ok {
		t.Error("only INBOX and Sent get cursors in M3")
	}
}

func TestLearnDryRunWritesNoCursor(t *testing.T) {
	cfg := learnConfig(t, "")
	st := openStore(t)
	if _, err := Learn(context.Background(), cfg, miniMailbox(), st, LearnOptions{Now: learnNow, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Cursor(context.Background(), "INBOX"); ok {
		t.Error("dry run stored a cursor")
	}
}

func TestLearnScopedKeepsCursors(t *testing.T) {
	cfg := learnConfig(t, "")
	fake := miniMailbox()
	st := openStore(t)
	if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow, Yes: true}); err != nil {
		t.Fatal(err)
	}
	first, _, _ := st.Cursor(context.Background(), "INBOX")
	fake.Append("INBOX", hdr("later@x.test", 0, "<later>"))
	if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow, Folder: "INBOX"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.Cursor(context.Background(), "INBOX"); got != first {
		t.Errorf("re-learning one folder moved the cursor: %+v -> %+v", first, got)
	}
}
