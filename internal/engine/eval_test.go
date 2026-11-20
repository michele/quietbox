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

func TestEvalHarmSplit(t *testing.T) {
	rep := runEval(t, evalMailbox())
	// hidden: the newsletter left in INBOX. leaked: quiet@ and leak@ x2 stay in
	// INBOX although filed. No message is filed in the wrong Sane folder.
	if h := rep.Harm; h.Hidden != 1 || h.Leaked != 3 || h.CrossFiled != 0 {
		t.Errorf("harm = %+v, want hidden 1, leaked 3, cross-filed 0", h)
	}
	if rep.Harm.Hidden != rep.HiddenImportant {
		t.Errorf("Harm.Hidden %d != HiddenImportant %d", rep.Harm.Hidden, rep.HiddenImportant)
	}
	if rep.InboxAgreement.Total != 12 || rep.InboxAgreement.Agree != 8 {
		t.Errorf("inbox-vs-not agreement = %+v, want 8/12", rep.InboxAgreement)
	}
	if got := rep.HiddenByRule[classify.RuleNewsletter]; got != 1 || len(rep.HiddenByRule) != 1 {
		t.Errorf("HiddenByRule = %v", rep.HiddenByRule)
	}
}

func TestEvalByRuleAndStrength(t *testing.T) {
	rep := runEval(t, evalMailbox())
	want := map[RuleKey]struct {
		total, agree int
		actual       map[string]int
	}{
		{classify.RuleNewsletter, classify.Strong}: {4, 3, map[string]int{"SaneNews": 3, "INBOX": 1}},
		{classify.RuleLater, classify.Weak}:        {1, 1, map[string]int{"SaneLater": 1}},
		{classify.RuleNone, classify.None}:         {4, 1, map[string]int{"INBOX": 1, "SaneNews": 1, "SaneBulk": 2}},
		{classify.RuleThread, classify.None}:       {1, 1, map[string]int{"INBOX": 1}},
		{classify.RuleSender, classify.None}:       {2, 2, map[string]int{"SaneNews": 1, "INBOX": 1}},
	}
	if len(rep.ByRuleStrength) != len(want) {
		t.Errorf("ByRuleStrength has %d keys, want %d: %v", len(rep.ByRuleStrength), len(want), rep.ByRuleStrength)
	}
	for k, w := range want {
		got := rep.ByRuleStrength[k]
		if got.Total != w.total || got.Agree != w.agree {
			t.Errorf("%v: %d/%d, want %d/%d", k, got.Agree, got.Total, w.agree, w.total)
		}
		for f, n := range w.actual {
			if got.Actual[f] != n {
				t.Errorf("%v: actual[%s] = %d, want %d", k, f, got.Actual[f], n)
			}
		}
	}
}

func TestEvalSignalHistogramCoversNewSenderDetectorsOnly(t *testing.T) {
	rep := runEval(t, evalMailbox())
	want := []SignalCount{
		{classify.RuleNewsletter, classify.Strong, "SaneNews", "list-id present; list-unsubscribe present", 3},
		{classify.RuleLater, classify.Weak, "SaneLater", "precedence bulk", 1},
		{classify.RuleNewsletter, classify.Strong, "INBOX", "list-id present; list-unsubscribe present", 1},
	}
	if len(rep.Signals) != len(want) {
		t.Fatalf("Signals = %+v", rep.Signals)
	}
	for i, w := range want {
		if rep.Signals[i] != w {
			t.Errorf("Signals[%d] = %+v, want %+v", i, rep.Signals[i], w)
		}
	}
}

// harmMailbox: three known senders whose history says one thing and whose
// post-cutoff mail sits elsewhere.
func harmMailbox() *mailtest.Fake {
	return &mailtest.Fake{
		Caps: mail.Capabilities{"IMAP4REV1"},
		Folders: []mail.Folder{
			{Name: "INBOX"}, {Name: "SaneLater"}, {Name: "SaneNews"}, {Name: "SaneBulk"}, {Name: "SaneBlackHole"},
			{Name: "Sent", Attrs: []string{`\Sent`}},
		},
		Messages: map[string][]mail.Header{
			"INBOX": {
				// mixed sender: SaneLater ~60%, INBOX ~40%
				newMail("m@mix.test", 99, "<i1>", nil), newMail("m@mix.test", 94, "<i2>", nil),
				newMail("m@mix.test", 20, "<i3>", nil),  // hidden, via a mixed sender's route
				newMail("b@bulk.test", 21, "<i4>", nil), // hidden, via a plain route
			},
			"SaneLater": {
				newMail("m@mix.test", 100, "<l1>", nil), newMail("m@mix.test", 95, "<l2>", nil), newMail("m@mix.test", 90, "<l3>", nil),
				newMail("k@x.test", 100, "<l4>", nil), newMail("k@x.test", 95, "<l5>", nil), newMail("k@x.test", 90, "<l6>", nil),
			},
			"SaneBulk": {
				newMail("b@bulk.test", 100, "<b1>", nil), newMail("b@bulk.test", 95, "<b2>", nil), newMail("b@bulk.test", 90, "<b3>", nil),
				newMail("k@x.test", 22, "<b4>", nil), // history says SaneLater: filed in the wrong Sane folder
			},
			"Sent": {},
		},
	}
}

func TestEvalHarmCategoriesAndMixedSenders(t *testing.T) {
	rep := runEval(t, harmMailbox())
	if rep.Overall.Total != 3 || rep.Known.Total != 3 {
		t.Fatalf("overall %+v known %+v, want 3 known messages", rep.Overall, rep.Known)
	}
	if h := rep.Harm; h.Hidden != 2 || h.Leaked != 0 || h.CrossFiled != 1 {
		t.Errorf("harm = %+v, want hidden 2, leaked 0, cross-filed 1", h)
	}
	if rep.HiddenByRule[classify.RuleSender] != 2 || rep.HiddenSenderMixed != 1 {
		t.Errorf("hidden by rule %v, of which mixed senders %d; want sender 2, mixed 1", rep.HiddenByRule, rep.HiddenSenderMixed)
	}
	// Filing in the wrong Sane folder is still "not in INBOX": only the two
	// hidden messages disagree on the inbox question.
	if rep.InboxAgreement.Total != 3 || rep.InboxAgreement.Agree != 1 {
		t.Errorf("inbox-vs-not = %+v, want 1/3", rep.InboxAgreement)
	}
}

func TestEvalReportShowsTheBreakdown(t *testing.T) {
	var buf bytes.Buffer
	if err := runEval(t, evalMailbox()).Write(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"hidden important", "leaked", "cross-filed", "inbox vs not", "By rule and strength",
		"new-sender detector decisions", "list-id present; list-unsubscribe present", "precedence bulk",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
	// The breakdown is meant to be pasted: no addresses.
	if strings.Contains(out, "@") {
		t.Errorf("report contains an address:\n%s", out)
	}
}

// policyMailbox: one known sender whose mail is a reply to something you sent (used
// for the diagnostics), and one mixed sender whose strong bulk match the
// mixed-override experiment acts on.
func policyMailbox() *mailtest.Fake {
	replyToMe := func(h *mail.Header) { h.InReplyTo = "<sent-h>" }
	return &mailtest.Fake{
		Caps: mail.Capabilities{"IMAP4REV1"},
		Folders: []mail.Folder{
			{Name: "INBOX"}, {Name: "SaneLater"}, {Name: "SaneNews"}, {Name: "SaneBulk"}, {Name: "SaneBlackHole"},
			{Name: "Sent", Attrs: []string{`\Sent`}},
		},
		Messages: map[string][]mail.Header{
			"INBOX": {
				// orders@mix.test: mixed sender, SaneLater ~60% / INBOX ~40%
				newMail("orders@mix.test", 99, "<m1>", nil), newMail("orders@mix.test", 94, "<m2>", nil),
				// reply to mail you sent, from a sender filed in SaneBulk: the sender route hides it
				newMail("h@help.test", 20, "<t1>", replyToMe),
			},
			"SaneLater": {
				newMail("orders@mix.test", 100, "<l1>", nil), newMail("orders@mix.test", 95, "<l2>", nil), newMail("orders@mix.test", 90, "<l3>", nil),
			},
			"SaneBulk": {
				newMail("h@help.test", 100, "<b1>", nil), newMail("h@help.test", 95, "<b2>", nil), newMail("h@help.test", 90, "<b3>", nil),
				// mixed sender, strong bulk match: only the override experiment files it here
				newMail("orders@mix.test", 21, "<t2>", func(h *mail.Header) { h.Subject = "Your order has shipped" }),
				// new sender, weak bulk match (noreply-style sender only)
				newMail("alerts@forge.test", 22, "<t3>", nil),
			},
			"Sent": {hdr("me@example.test", 70, "<sent-h>", "x@y.test")},
		},
	}
}

func evalWith(t *testing.T, p classify.Policy) *EvalReport {
	t.Helper()
	rep, err := Eval(context.Background(), learnConfig(t, ""), policyMailbox(), EvalOptions{Cutoff: evalCutoff, Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestEvalMixedOverrideExperiment(t *testing.T) {
	// Default: the mixed sender follows its majority route (SaneLater) although the
	// message sits in SaneBulk: filed in the wrong Sane folder, out of INBOX either way.
	def := evalWith(t, classify.Policy{})
	if def.Overall.Total != 3 || def.Overall.Agree != 1 || def.Harm != (HarmCounts{Hidden: 1, CrossFiled: 1}) {
		t.Errorf("default: agree %d/%d harm %+v, want 1/3 with hidden 1, cross-filed 1", def.Overall.Agree, def.Overall.Total, def.Harm)
	}
	// Experiment: the strong bulk match overrides the route and agrees.
	exp := evalWith(t, classify.Policy{MixedOverride: true})
	if exp.Overall.Agree != 2 || exp.Harm != (HarmCounts{Hidden: 1}) {
		t.Errorf("mixed-override: agree %d harm %+v, want 2/3 with hidden 1", exp.Overall.Agree, exp.Harm)
	}
	if exp.Policy != (classify.Policy{MixedOverride: true}) || def.Policy != (classify.Policy{}) {
		t.Errorf("report policies = %+v / %+v", def.Policy, exp.Policy)
	}
}

func TestEvalReportNamesActiveExperiments(t *testing.T) {
	var plain, exp bytes.Buffer
	_ = evalWith(t, classify.Policy{}).Write(&plain)
	_ = evalWith(t, classify.Policy{MixedOverride: true}).Write(&exp)
	if strings.Contains(plain.String(), "Experiment") {
		t.Errorf("default run must not mention experiments:\n%s", plain.String())
	}
	if !strings.Contains(exp.String(), "Experiment: mixed-override (not the defaults)") {
		t.Errorf("experiment run must name its policy:\n%s", exp.String())
	}
}

func TestEvalHiddenKnownSenderDiagnostics(t *testing.T) {
	rep := evalWith(t, classify.Policy{})
	hk := rep.HiddenKnown
	// One hidden message: a reply to your mail from h@help.test (route share 100%,
	// 3 messages of history), not a mixed sender.
	if hk.Total != 1 || hk.Replies != 1 || hk.InMyThread != 1 || hk.Mixed != 0 {
		t.Errorf("HiddenKnown = %+v", hk)
	}
	if hk.ShareHigh != 1 || hk.ShareMid != 0 || hk.ShareLow != 0 {
		t.Errorf("route share buckets = %+v", hk)
	}
	if hk.Hist3to9 != 1 || hk.Hist1to2 != 0 || hk.Hist10Plus != 0 {
		t.Errorf("history buckets = %+v", hk)
	}
	var buf bytes.Buffer
	_ = rep.Write(&buf)
	for _, want := range []string{"known senders (1)", "replies 1", "in a thread you started 1", "route share", "history"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestEvalHiddenKnownBucketsMixedAndNonReplies(t *testing.T) {
	// harmMailbox hides m@mix.test (mixed, ~60% route, 5 messages) and b@bulk.test
	// (route 100%, 3 messages); neither is a reply.
	hk := runEval(t, harmMailbox()).HiddenKnown
	if hk.Total != 2 || hk.Replies != 0 || hk.InMyThread != 0 || hk.Mixed != 1 {
		t.Errorf("HiddenKnown = %+v", hk)
	}
	if hk.ShareHigh != 1 || hk.ShareMid != 1 || hk.ShareLow != 0 {
		t.Errorf("route share buckets = %+v", hk)
	}
	if hk.Hist3to9 != 2 {
		t.Errorf("history buckets = %+v", hk)
	}
}

// The thread rule protects replies to your mail from people you do not know yet.
// If message-ID matching silently broke on real mail that protection would be gone
// without any visible symptom, so the report shows the numbers behind it.
func TestEvalThreadSanityLine(t *testing.T) {
	rep := runEval(t, evalMailbox())
	// Only <sent-pre> was sent before the cutoff; one test message replies to it
	// (replier@else.test). The reply to <sent-post> must not count.
	if rep.SentIDs != 1 || rep.RepliesToSent != 1 {
		t.Errorf("SentIDs %d, RepliesToSent %d; want 1 and 1", rep.SentIDs, rep.RepliesToSent)
	}
	var buf bytes.Buffer
	_ = rep.Write(&buf)
	if want := "Thread check: 1 sent Message-IDs learned before the cutoff; 1 of the 12 test messages reply to one of them"; !strings.Contains(buf.String(), want) {
		t.Errorf("report lacks %q:\n%s", want, buf.String())
	}
}

func TestEvalThreadSanityLineCountsAnySender(t *testing.T) {
	// h@help.test is a known sender and replies to <sent-h>: it counts even though
	// the thread rule never needs to decide it.
	rep := evalWith(t, classify.Policy{})
	if rep.SentIDs != 1 || rep.RepliesToSent != 1 {
		t.Errorf("SentIDs %d, RepliesToSent %d; want 1 and 1", rep.SentIDs, rep.RepliesToSent)
	}
}
