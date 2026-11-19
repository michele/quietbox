package engine

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strings"
	"testing"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/mail/mailtest"
)

// evalCutoff is 60 days before learnNow. Mail older than this is history the
// eval may learn from; newer mail is the test set.
var evalCutoff = daysAgo(60)

func listHeaders(h *mail.Header) {
	h.ListID = "Weekly <weekly.test>"
	sig(h, "list-unsubscribe", "<https://weekly.test/u>")
}

// evalMailbox is a timeline with deliberate traps for leakage between the
// history and the test set (see the comments on each message).
func evalMailbox() *mailtest.Fake {
	subj := func(h *mail.Header) { h.Subject = secretSubject }
	both := func(fs ...func(*mail.Header)) func(*mail.Header) {
		return func(h *mail.Header) {
			for _, f := range fs {
				f(h)
			}
		}
	}
	return &mailtest.Fake{
		Caps: mail.Capabilities{"IMAP4REV1"},
		Folders: []mail.Folder{
			{Name: "INBOX"}, {Name: "SaneLater"}, {Name: "SaneNews"}, {Name: "SaneBulk"}, {Name: "SaneBlackHole"},
			{Name: "Sent", Attrs: []string{`\Sent`}},
		},
		Messages: map[string][]mail.Header{
			"INBOX": {
				// history
				newMail("human@friends.test", 95, "<h1>", subj), newMail("human@friends.test", 85, "<h2>", subj),
				newMail("ancient@old.test", 360, "<anc>", subj), // outside the inbox lookback at the cutoff
				// test set
				newMail("human@friends.test", 25, "<t2>", subj),                                                                        // known, agrees
				newMail("plain@stranger.test", 24, "<t4>", subj),                                                                       // new, nothing fires, agrees
				newMail("sneaky@promo.test", 23, "<t7>", both(subj, listHeaders)),                                                      // new, INBOX but looks like a newsletter: hidden important
				newMail("replier@else.test", 22, "<t8>", both(subj, listHeaders, func(h *mail.Header) { h.InReplyTo = "<sent-pre>" })), // thread from before the cutoff
				newMail("me@example.test", 21, "<self>", subj),                                                                         // skipped: own address
				{InternalDate: daysAgo(20), Date: daysAgo(20), MessageID: "<nofrom>"},                                                  // skipped: no From
			},
			"SaneNews": {
				newMail("known@paper.test", 100, "<k1>", subj), newMail("known@paper.test", 90, "<k2>", subj), newMail("known@paper.test", 80, "<k3>", subj),
				newMail("known@paper.test", 30, "<t1>", subj),                                                                            // known, agrees
				newMail("newbie@weekly.test", 29, "<t3>", both(subj, listHeaders)),                                                       // new, newsletter detector agrees
				newMail("quiet@plainlooking.test", 28, "<t5>", subj),                                                                     // new, filed as news but looks human: disagrees
				newMail("replier2@else.test", 27, "<t9>", both(subj, listHeaders, func(h *mail.Header) { h.InReplyTo = "<sent-post>" })), // reply to mail sent AFTER the cutoff: no thread signal
				newMail("ancient@old.test", 26, "<t11>", both(subj, listHeaders)),                                                        // must be new: its only history is outside the lookback
			},
			"SaneBulk": {
				newMail("leak@bulky.test", 22, "<t6a>", subj), // only exists after the cutoff: must not be learned
				newMail("leak@bulky.test", 21, "<t6b>", subj),
			},
			"SaneLater": {
				newMail("later@w.test", 19, "<t10>", both(subj, func(h *mail.Header) { sig(h, "precedence", "bulk") })), // I wrote to them only after the cutoff
			},
			"SaneBlackHole": {},
			"Sent": {
				hdr("me@example.test", 70, "<sent-pre>", "client@biz.test"),
				hdr("me@example.test", 18, "<sent-post>", "later@w.test"),
			},
		},
	}
}

func runEval(t *testing.T, fake *mailtest.Fake) *EvalReport {
	t.Helper()
	rep, err := Eval(context.Background(), learnConfig(t, ""), fake, EvalOptions{Cutoff: evalCutoff})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func evalRow(t *testing.T, rep *EvalReport, msgFrom string, n int) EvalRow {
	t.Helper()
	seen := 0
	for _, r := range rep.Rows {
		if r.From == msgFrom {
			if seen == n {
				return r
			}
			seen++
		}
	}
	t.Fatalf("no row #%d for %s", n, msgFrom)
	return EvalRow{}
}

func TestEvalAgreementNumbers(t *testing.T) {
	rep := runEval(t, evalMailbox())

	if rep.Overall.Total != 12 || rep.Overall.Agree != 8 {
		t.Errorf("overall = %+v, want 8/12", rep.Overall)
	}
	if rep.Known.Total != 2 || rep.Known.Agree != 2 {
		t.Errorf("known = %+v, want 2/2", rep.Known)
	}
	if rep.New.Total != 10 || rep.New.Agree != 6 {
		t.Errorf("new = %+v, want 6/10", rep.New)
	}
	if p := rep.New.Percent(); p != 60 {
		t.Errorf("new percent = %v", p)
	}
	if rep.SkippedSelf != 1 || rep.SkippedNoFrom != 1 {
		t.Errorf("skipped self/nofrom = %d/%d, want 1/1", rep.SkippedSelf, rep.SkippedNoFrom)
	}
	if rep.InboxTotal != 4 || rep.HiddenImportant != 1 || rep.HiddenImportantNew != 1 {
		t.Errorf("hidden important = %d of %d (new %d), want 1 of 4 (new 1)", rep.HiddenImportant, rep.InboxTotal, rep.HiddenImportantNew)
	}

	wantConf := map[string]map[string]int{
		"SaneNews":  {"SaneNews": 4, "INBOX": 1},
		"SaneBulk":  {"INBOX": 2},
		"INBOX":     {"INBOX": 3, "SaneNews": 1},
		"SaneLater": {"SaneLater": 1},
	}
	for actual, cols := range wantConf {
		for pred, n := range cols {
			if got := rep.Confusion[actual][pred]; got != n {
				t.Errorf("confusion[%s][%s] = %d, want %d", actual, pred, got, n)
			}
		}
	}

	wantRule := map[classify.Rule]Agreement{
		classify.RuleSender:     {Total: 2, Agree: 2},
		classify.RuleNewsletter: {Total: 4, Agree: 3},
		classify.RuleNone:       {Total: 4, Agree: 1},
		classify.RuleThread:     {Total: 1, Agree: 1},
		classify.RuleLater:      {Total: 1, Agree: 1},
	}
	for rule, want := range wantRule {
		if got := rep.ByRule[rule]; got != want {
			t.Errorf("rule %s = %+v, want %+v", rule, got, want)
		}
	}
}

// Each of these would flip if the eval learned from, or counted, the test set.
func TestEvalDoesNotLeakTheTestSetIntoLearning(t *testing.T) {
	rep := runEval(t, evalMailbox())
	cases := []struct {
		from      string
		new       bool
		predicted string
		rule      classify.Rule
		why       string
	}{
		{"leak@bulky.test", true, "INBOX", classify.RuleNone, "a sender seen only after the cutoff is not learned"},
		{"later@w.test", true, "SaneLater", classify.RuleLater, "mail you sent after the cutoff does not make a sender known"},
		{"replier2@else.test", true, "SaneNews", classify.RuleNewsletter, "a reply to mail sent after the cutoff is not a thread"},
		{"replier@else.test", true, "INBOX", classify.RuleThread, "a reply to mail sent before the cutoff is a thread"},
		{"ancient@old.test", true, "SaneNews", classify.RuleNewsletter, "INBOX mail older than the lookback at the cutoff is not history"},
		{"known@paper.test", false, "SaneNews", classify.RuleSender, "pre-cutoff history is used"},
	}
	for _, c := range cases {
		r := evalRow(t, rep, c.from, 0)
		if r.NewSender != c.new || r.Decision.Folder != c.predicted || r.Decision.Rule != c.rule {
			t.Errorf("%s: new=%v %s via %s, want new=%v %s via %s (%s)", c.from, r.NewSender,
				r.Decision.Folder, r.Decision.Rule, c.new, c.predicted, c.rule, c.why)
		}
	}
}

func TestEvalNeverTouchesTheMailbox(t *testing.T) {
	fake := evalMailbox()
	runEval(t, fake)
	if len(fake.Created) != 0 {
		t.Errorf("eval created folders: %v", fake.Created)
	}
}

func TestEvalErrors(t *testing.T) {
	cfg := learnConfig(t, "")
	ctx := context.Background()
	if _, err := Eval(ctx, cfg, evalMailbox(), EvalOptions{}); err == nil || !strings.Contains(err.Error(), "cutoff") {
		t.Errorf("zero cutoff: %v", err)
	}
	if _, err := Eval(ctx, cfg, evalMailbox(), EvalOptions{Cutoff: daysAgo(-5)}); err == nil || !strings.Contains(err.Error(), "no messages") {
		t.Errorf("cutoff after all mail: %v", err)
	}
	fake := evalMailbox()
	fake.FetchErr = errors.New("boom")
	if _, err := Eval(ctx, cfg, fake, EvalOptions{Cutoff: evalCutoff}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("fetch error: %v", err)
	}
}

func TestEvalReportText(t *testing.T) {
	rep := runEval(t, evalMailbox())
	var buf bytes.Buffer
	if err := rep.Write(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"8 of 12", "known senders", "new senders", "6 of 10", "60%",
		"Hidden important", "1 of 4", "newsletter", "nothing was changed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secretSubject) {
		t.Error("report leaks a subject")
	}
}

func TestEvalCSVHoldsOnlyDisagreements(t *testing.T) {
	rep := runEval(t, evalMailbox())
	var buf bytes.Buffer
	if err := rep.WriteCSV(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), secretSubject) {
		t.Error("csv leaks a subject")
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rows[0], ","); got != "date,from,actual,predicted,rule,new_sender,reason,signals" {
		t.Errorf("header = %s", got)
	}
	if len(rows) != 1+4 { // quiet, leak x2, sneaky
		t.Fatalf("rows = %d, want 4 disagreements", len(rows)-1)
	}
	for _, r := range rows[1:] {
		if r[2] == r[3] {
			t.Errorf("agreeing row in disagreement csv: %v", r)
		}
	}
}

func TestAgreementPercent(t *testing.T) {
	if p := (Agreement{}).Percent(); p != 0 {
		t.Errorf("empty = %v", p)
	}
	if p := (Agreement{Total: 3, Agree: 1}).Percent(); p < 33.3 || p > 33.4 {
		t.Errorf("1/3 = %v", p)
	}
}
