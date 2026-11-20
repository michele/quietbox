package engine

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
	"github.com/michele/quietbox/internal/store"
)

const secretSubject = "SECRET-SUBJECT-DO-NOT-PRINT"

// newMail builds an INBOX message that arrived after the learn run.
func newMail(from string, days int, mid string, mod func(*mail.Header)) mail.Header {
	h := mail.Header{From: from, Date: daysAgo(days), InternalDate: daysAgo(days), MessageID: mid, Subject: secretSubject}
	if mod != nil {
		mod(&h)
	}
	return h
}

func sig(h *mail.Header, kv ...string) {
	if h.Signals == nil {
		h.Signals = map[string]string{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Signals[kv[i]] = kv[i+1]
	}
}

// learnedMailbox runs a full learn on miniMailbox and then delivers new mail to
// INBOX, like a real mailbox between two runs.
func learnedMailbox(t *testing.T) (*mailtest.Fake, *store.Store, *config.Config) {
	t.Helper()
	cfg := learnConfig(t, "")
	fake := miniMailbox()
	st := openStore(t)
	if _, err := Learn(context.Background(), cfg, fake, st, LearnOptions{Now: learnNow}); err != nil {
		t.Fatal(err)
	}
	fake.Messages["INBOX"] = append(fake.Messages["INBOX"],
		newMail("new1@substack.test", 1, "<n1>", nil),
		newMail("newsletter@weekly.test", 1, "<n2>", func(h *mail.Header) {
			h.ListID = "Weekly <weekly.test>"
			sig(h, "list-unsubscribe", "<https://weekly.test/u>")
		}),
		newMail("noreply@shop.test", 1, "<n3>", func(h *mail.Header) { h.Subject = "Your order has shipped" }),
		newMail("promo@store2.test", 1, "<n4>", func(h *mail.Header) { sig(h, "precedence", "bulk") }),
		newMail("stranger@x.test", 1, "<n5>", nil),
		newMail("spam@pushy.test", 1, "<n6>", nil),
		newMail("anon@elsewhere.test", 1, "<n7>", func(h *mail.Header) { h.InReplyTo = "<sent1>" }),
		newMail("store@big.test", 1, "<n8>", func(h *mail.Header) { // mixed sender + strong newsletter signals
			h.ListID = "Big <big.test>"
			sig(h, "list-unsubscribe", "<https://big.test/u>")
		}),
		newMail("new1@substack.test", 1, "<n1>", nil), // duplicate delivery of <n1>
		newMail("old2@x.test", 300, "<n9>", nil),      // older than the lookback
		newMail("me@example.test", 1, "<n10>", nil),   // your own address
	)
	return fake, st, cfg
}

func scanned(t *testing.T, o ScanOptions) (*ScanReport, *mailtest.Fake, *store.Store) {
	t.Helper()
	fake, st, cfg := learnedMailbox(t)
	o.Now = learnNow
	rep, err := Scan(context.Background(), cfg, fake, st, o)
	if err != nil {
		t.Fatal(err)
	}
	return rep, fake, st
}

func rowFor(t *testing.T, rep *ScanReport, from string) ScanRow {
	t.Helper()
	for _, r := range rep.Rows {
		if r.From == from {
			return r
		}
	}
	t.Fatalf("no row for %s", from)
	return ScanRow{}
}

func TestScanClassifiesInbox(t *testing.T) {
	rep, _, _ := scanned(t, ScanOptions{})
	want := map[string]struct {
		folder string
		rule   classify.Rule
	}{
		"new1@substack.test":     {"SaneNews", classify.RuleDomain},
		"newsletter@weekly.test": {"SaneNews", classify.RuleNewsletter},
		"noreply@shop.test":      {"SaneBulk", classify.RuleBulk},
		"promo@store2.test":      {"SaneLater", classify.RuleLater},
		"stranger@x.test":        {"INBOX", classify.RuleNone},
		"spam@pushy.test":        {"SaneBlackHole", classify.RuleBlackhole},
		"anon@elsewhere.test":    {"INBOX", classify.RuleThread},
		"store@big.test":         {"SaneBulk", classify.RuleSender}, // mixed sender: majority route, the override is off by default
		"me@example.test":        {"INBOX", classify.RuleNone},
		"alice@friends.test":     {"INBOX", classify.RuleSender},
	}
	for from, w := range want {
		got := rowFor(t, rep, from)
		if got.Decision.Folder != w.folder || got.Decision.Rule != w.rule {
			t.Errorf("%s: %s via %s (%s), want %s via %s", from, got.Decision.Folder, got.Decision.Rule, got.Decision.Reason, w.folder, w.rule)
		}
	}
}

func TestScanSkipsOldAndDuplicates(t *testing.T) {
	rep, _, _ := scanned(t, ScanOptions{})
	n := 0
	for _, r := range rep.Rows {
		switch r.From {
		case "old2@x.test":
			t.Error("message older than inbox_lookback was scanned")
		case "new1@substack.test":
			n++
		}
	}
	if n != 1 {
		t.Errorf("duplicate delivery produced %d rows, want 1", n)
	}
}

func TestScanSinceOverridesLookback(t *testing.T) {
	rep, _, _ := scanned(t, ScanOptions{Since: 2 * 24 * time.Hour})
	for _, r := range rep.Rows {
		if r.Date.Before(daysAgo(2)) {
			t.Errorf("%s dated %v is older than --since", r.From, r.Date)
		}
	}
	if len(rep.Rows) == 0 {
		t.Fatal("no rows")
	}
}

func TestScanSummary(t *testing.T) {
	rep, _, _ := scanned(t, ScanOptions{})
	if rep.Inbox != "INBOX" {
		t.Errorf("Inbox = %q", rep.Inbox)
	}
	total := 0
	for _, n := range rep.ByFolder {
		total += n
	}
	if total != len(rep.Rows) {
		t.Errorf("ByFolder sums to %d, rows = %d", total, len(rep.Rows))
	}
	ruleTotal := 0
	for _, n := range rep.ByRule {
		ruleTotal += n
	}
	if ruleTotal != len(rep.Rows) {
		t.Errorf("ByRule sums to %d, rows = %d", ruleTotal, len(rep.Rows))
	}
	moves := 0
	for _, r := range rep.Rows {
		if r.Decision.Folder != "INBOX" {
			moves++
		}
	}
	if rep.Moves() != moves || moves == 0 {
		t.Errorf("Moves() = %d, want %d (>0)", rep.Moves(), moves)
	}
	for i := 1; i < len(rep.Rows); i++ {
		if rep.Rows[i].Date.After(rep.Rows[i-1].Date) {
			t.Fatal("rows are not newest first")
		}
	}
}

func TestScanChangesNothing(t *testing.T) {
	fake, st, cfg := learnedMailbox(t)
	ctx := context.Background()
	auditBefore, _ := st.Audit(ctx)
	sendersBefore, _ := st.Senders(ctx, store.SenderFilter{})
	idsBefore, _ := st.SentMessageIDs(ctx)
	obsBefore, _ := st.Observations(ctx)

	if _, err := Scan(ctx, cfg, fake, st, ScanOptions{Now: learnNow}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Created) != 0 {
		t.Errorf("scan created folders: %v", fake.Created)
	}
	auditAfter, _ := st.Audit(ctx)
	sendersAfter, _ := st.Senders(ctx, store.SenderFilter{})
	idsAfter, _ := st.SentMessageIDs(ctx)
	obsAfter, _ := st.Observations(ctx)
	if len(auditAfter) != len(auditBefore) || len(sendersAfter) != len(sendersBefore) ||
		len(idsAfter) != len(idsBefore) || len(obsAfter) != len(obsBefore) {
		t.Error("scan wrote to the database")
	}
}

func TestScanRequiresALearnedDatabase(t *testing.T) {
	cfg := learnConfig(t, "")
	_, err := Scan(context.Background(), cfg, miniMailbox(), openStore(t), ScanOptions{Now: learnNow})
	if err == nil || !strings.Contains(err.Error(), "init") {
		t.Fatalf("err = %v, want a hint to run quietbox init", err)
	}
	if _, err := Scan(context.Background(), cfg, miniMailbox(), nil, ScanOptions{Now: learnNow}); err == nil {
		t.Error("nil store must be an error")
	}
}

func TestScanPropagatesFetchErrors(t *testing.T) {
	fake, st, cfg := learnedMailbox(t)
	fake.FetchErr = errors.New("boom")
	if _, err := Scan(context.Background(), cfg, fake, st, ScanOptions{Now: learnNow}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestScanReportsNeverContainSubjects(t *testing.T) {
	rep, _, _ := scanned(t, ScanOptions{})
	var text, csvOut bytes.Buffer
	if err := rep.Write(&text, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteCSV(&csvOut); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": text.String(), "csv": csvOut.String()} {
		if strings.Contains(out, secretSubject) || strings.Contains(out, "Your order has shipped") {
			t.Errorf("%s report leaks a subject:\n%s", name, out)
		}
	}
}

func TestScanWriteLimitAndAll(t *testing.T) {
	rep, _, _ := scanned(t, ScanOptions{})
	var all, limited, movesOnly bytes.Buffer
	_ = rep.Write(&all, 0, true)
	_ = rep.Write(&limited, 1, false)
	_ = rep.Write(&movesOnly, 0, false)
	if !strings.Contains(all.String(), "stranger@x.test") {
		t.Error("--all should list messages that stay")
	}
	if strings.Contains(movesOnly.String(), "stranger@x.test") {
		t.Error("default listing must show only messages that would move")
	}
	if !strings.Contains(limited.String(), "more") {
		t.Errorf("limited listing should say how many are hidden:\n%s", limited.String())
	}
	for _, out := range []string{all.String(), movesOnly.String()} {
		if !strings.Contains(out, "nothing was changed") {
			t.Errorf("report must say it changed nothing:\n%s", out)
		}
	}
}

func TestScanCSV(t *testing.T) {
	rep, _, _ := scanned(t, ScanOptions{})
	var buf bytes.Buffer
	if err := rep.WriteCSV(&buf); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(rep.Rows)+1 {
		t.Fatalf("csv rows = %d, want %d", len(rows), len(rep.Rows)+1)
	}
	head := strings.Join(rows[0], ",")
	if head != "date,from,folder,rule,would_move,reason,signals" {
		t.Errorf("header = %s", head)
	}
	for _, r := range rows[1:] {
		if r[1] == "newsletter@weekly.test" && (r[2] != "SaneNews" || r[3] != "newsletter" || r[4] != "true" || r[6] == "") {
			t.Errorf("row = %v", r)
		}
	}
}

func TestKnowledgeFromStore(t *testing.T) {
	_, st, cfg := learnedMailbox(t)
	k, targets, err := KnowledgeFromStore(context.Background(), cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if k.Inbox != "INBOX" || k.Blackhole != "SaneBlackHole" {
		t.Errorf("Inbox/Blackhole = %q/%q", k.Inbox, k.Blackhole)
	}
	if r := k.Senders["alice@friends.test"]; r.Folder != "INBOX" || r.Reason == "" {
		t.Errorf("alice = %+v", r)
	}
	if !k.Senders["store@big.test"].Mixed {
		t.Error("store@big.test should be mixed")
	}
	if k.Domains["substack.test"] != "SaneNews" {
		t.Errorf("domains = %v", k.Domains)
	}
	if !k.SentTo["dave@old.test"] {
		t.Errorf("SentTo = %v", k.SentTo)
	}
	if _, ok := k.SentIDs["<sent1>"]; !ok {
		t.Errorf("SentIDs = %v", k.SentIDs)
	}
	if !k.Me["me@example.test"] {
		t.Errorf("Me = %v", k.Me)
	}
	if targets[config.TypeNewsletter] != "SaneNews" || targets[config.TypeBulk] != "SaneBulk" || targets[config.TypeLater] != "SaneLater" {
		t.Errorf("targets = %v", targets)
	}
}
