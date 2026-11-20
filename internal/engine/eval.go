package engine

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/learn"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// EvalOptions controls Eval.
type EvalOptions struct {
	// Cutoff splits the mailbox: mail that arrived before it is history to learn
	// from, mail on or after it is the test set. Required.
	Cutoff time.Time
	// History limits how far before the cutoff to learn from (0 = no limit).
	// INBOX is always capped by learn.inbox_lookback, measured from the cutoff.
	History time.Duration
	// Policy switches on classification experiments (see classify.Policy). The
	// zero value is the default behaviour.
	Policy   classify.Policy
	Progress func(msg string)
}

// Agreement counts messages where quietbox would file mail where it already is.
type Agreement struct {
	Total int
	Agree int
}

// Percent is Agree/Total in percent, 0 when there is nothing to count.
func (a Agreement) Percent() float64 {
	if a.Total == 0 {
		return 0
	}
	return 100 * float64(a.Agree) / float64(a.Total)
}

func (a *Agreement) add(agree bool) {
	a.Total++
	if agree {
		a.Agree++
	}
}

// EvalRow is one test message. It holds no subject and no body.
type EvalRow struct {
	Date      time.Time
	From      string
	Actual    string // the folder the message is in
	Decision  classify.Decision
	NewSender bool // nothing in the history (and no mail sent to them) before the cutoff
	Mixed     bool // the sender's learned route is mixed

	// Diagnostics for known senders: why might their mail be misfiled?
	Reply      bool    // has In-Reply-To or References
	InMyThread bool    // replies to mail you sent
	RouteShare float64 // history share of the sender's route folder (0 for new senders)
	History    int     // messages of history in the learning window (0 for new senders)
}

// HiddenKnown describes the INBOX messages from known senders that would have
// been filed away: the ones detector tuning cannot explain. Aggregate only.
type HiddenKnown struct {
	Total      int
	Replies    int // has In-Reply-To or References
	InMyThread int // replies to mail you sent (a thread-first rule would keep them)
	Mixed      int // the sender's route is mixed
	// Route share: how dominant the route folder was in the sender's history.
	ShareHigh, ShareMid, ShareLow int // >= 90%, 60-89%, < 60%
	// History: messages from the sender in the learning window.
	Hist1to2, Hist3to9, Hist10Plus int
}

// HarmCounts splits the disagreements by how much they hurt.
type HarmCounts struct {
	Hidden     int // in INBOX, quietbox would file it away: the serious one
	Leaked     int // filed somewhere, quietbox would leave it in INBOX: mild
	CrossFiled int // filed in a different Sane folder: out of INBOX either way
}

// RuleKey groups decisions by the rule that made them and, for detectors, how
// sure the detector was.
type RuleKey struct {
	Rule     classify.Rule
	Strength classify.Strength
}

// RuleStat is agreement for one RuleKey plus where those messages actually are.
type RuleStat struct {
	Agreement
	Actual map[string]int
}

// SignalCount is how often a detector decision for a new sender had exactly these
// signals while the message was actually in Actual. Signals come from the
// detectors' fixed vocabularies: no addresses, subjects or header text.
type SignalCount struct {
	Rule     classify.Rule
	Strength classify.Strength
	Actual   string
	Signals  string // sorted, joined with "; "
	Count    int
}

// Agrees reports whether the prediction matches where the message is.
func (r EvalRow) Agrees() bool { return r.Decision.Folder == r.Actual }

// EvalReport is the result of a hold-out evaluation. Nothing was changed.
type EvalReport struct {
	Cutoff  time.Time
	Inbox   string
	Folders []string // configured folders, in config order, for the matrix
	Rows    []EvalRow

	Overall, Known, New Agreement
	ByRule              map[classify.Rule]Agreement
	// Confusion[actual][predicted] counts messages.
	Confusion map[string]map[string]int

	// InboxTotal is how many test messages are in INBOX; HiddenImportant is how
	// many of those quietbox would have filed elsewhere (the worst failure).
	InboxTotal         int
	HiddenImportant    int
	HiddenImportantNew int

	Harm HarmCounts
	// InboxAgreement counts messages where "in INBOX or not" matches, ignoring
	// which Sane folder: the question that matters most.
	InboxAgreement Agreement
	// HiddenByRule says which rule would have hidden each INBOX message;
	// HiddenSenderMixed is the part of the sender rule that came from mixed senders.
	HiddenByRule      map[classify.Rule]int
	HiddenSenderMixed int
	ByRuleStrength    map[RuleKey]RuleStat
	Signals           []SignalCount // new-sender detector decisions, most common first

	Policy      classify.Policy // experiments that were switched on
	HiddenKnown HiddenKnown

	HistoryMessages int // messages learned from
	SkippedSelf     int // test messages from your own addresses
	SkippedNoFrom   int // test messages without a usable From
}

type testMsg struct {
	h      mail.Header
	folder string
}

// Eval learns only from mail that arrived before the cutoff, then classifies
// the mail that arrived after it and compares with the folder it is actually in.
// It is fully in memory: no database, and the mailbox is only read.
//
// Known senders agree almost by construction, so the report separates them
// from new senders (no history before the cutoff): that number measures the
// detectors.
func Eval(ctx context.Context, cfg *config.Config, b mail.Backend, o EvalOptions) (*EvalReport, error) {
	if o.Cutoff.IsZero() {
		return nil, errors.New("eval: a cutoff date is required")
	}
	progress := func(format string, a ...any) {
		if o.Progress != nil {
			o.Progress(fmt.Sprintf(format, a...))
		}
	}
	server, err := b.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	onServer := map[string]bool{"INBOX": true}
	for _, f := range server {
		onServer[f.Name] = true
	}
	sentName, err := sentFolder(cfg, server)
	if err != nil {
		return nil, err
	}
	me := map[string]bool{}
	for _, a := range cfg.Me.Addresses {
		me[a] = true
	}
	p := learnParams(cfg)
	rep := &EvalReport{
		Cutoff: o.Cutoff, Inbox: p.InboxFolder,
		ByRule: map[classify.Rule]Agreement{}, Confusion: map[string]map[string]int{},
		HiddenByRule: map[classify.Rule]int{}, ByRuleStrength: map[RuleKey]RuleStat{},
	}
	type sigKey struct {
		rule     classify.Rule
		strength classify.Strength
		actual   string
		signals  string
	}
	sigCounts := map[sigKey]int{}

	// 1. Read every configured folder once: before the cutoff is history, the
	// rest is the test set.
	var history []store.Obs
	var tests []testMsg
	for _, cf := range cfg.Folders {
		rep.Folders = append(rep.Folders, cf.Name)
		if !onServer[cf.Name] || cf.Name == sentName {
			continue
		}
		since := time.Time{}
		if o.History > 0 {
			since = o.Cutoff.Add(-o.History)
		}
		if cf.Type == config.TypeImportant {
			if lookback := o.Cutoff.Add(-cfg.Learn.InboxLookbackDur); since.IsZero() || lookback.After(since) {
				since = lookback
			}
		}
		progress("reading %s ...", cf.Name)
		seen := map[string]struct{}{}
		err := b.FetchHeaders(ctx, cf.Name, since, func(batch []mail.Header) error {
			for _, h := range batch {
				k := mail.Key(h)
				if _, dup := seen[k]; dup {
					continue
				}
				seen[k] = struct{}{}
				from := strings.ToLower(h.From)
				isTest := !h.InternalDate.Before(o.Cutoff)
				switch {
				case from == "":
					if isTest {
						rep.SkippedNoFrom++
					}
				case me[from]:
					if isTest {
						rep.SkippedSelf++
					}
				case isTest:
					tests = append(tests, testMsg{h: h, folder: cf.Name})
				default:
					history = append(history, store.Obs{Address: from, Folder: cf.Name, At: h.InternalDate})
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", cf.Name, err)
		}
	}
	rep.HistoryMessages = len(history)

	// 2. Sent mail: only what was sent before the cutoff counts.
	progress("reading %s (sent mail) ...", sentName)
	sentAgg := map[string]*store.SentRow{}
	sentIDs := map[string]struct{}{}
	var sentSince time.Time
	if o.History > 0 {
		sentSince = o.Cutoff.Add(-o.History)
	}
	seenSent := map[string]struct{}{}
	err = b.FetchHeaders(ctx, sentName, sentSince, func(batch []mail.Header) error {
		for _, h := range batch {
			k := mail.Key(h)
			if _, dup := seenSent[k]; dup {
				continue
			}
			seenSent[k] = struct{}{}
			when := sentWhen(h)
			if !when.Before(o.Cutoff) {
				continue
			}
			if h.MessageID != "" {
				sentIDs[h.MessageID] = struct{}{}
			}
			addSentRecipients(sentAgg, me, h, when)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", sentName, err)
	}
	var sentRows []store.SentRow
	for _, row := range sentAgg {
		sentRows = append(sentRows, *row)
	}
	sort.Slice(sentRows, func(i, j int) bool { return sentRows[i].Address < sentRows[j].Address })

	if len(tests) == 0 {
		return nil, fmt.Errorf("no messages to evaluate on or after %s (skipped %d from your own addresses, %d without a From)",
			o.Cutoff.Format("2006-01-02"), rep.SkippedSelf, rep.SkippedNoFrom)
	}

	// 3. Learn as of the cutoff, using the same code as `quietbox init`.
	routes, _, domains := resolveRoutes(history, sentRows, p, o.Cutoff)
	k := baseKnowledge(cfg)
	k.SentIDs = sentIDs
	k.Policy = o.Policy
	rep.Policy = o.Policy
	routeOf := map[string]learn.Route{}
	for _, r := range routes {
		k.Senders[r.Address] = classify.SenderRoute{Folder: r.Folder, Mixed: r.Mixed, Reason: r.Reason}
		routeOf[r.Address] = r
	}
	for _, d := range domains {
		k.Domains[d.Domain] = d.Folder
	}
	for _, r := range sentRows {
		k.SentTo[r.Address] = true
	}
	targets := classify.Targets(cfg.DetectionTargets())

	// 4. Classify the test set and compare.
	for _, t := range tests {
		d := classify.Classify(t.h, k, targets)
		from := strings.ToLower(t.h.From)
		route, known := k.Senders[from]
		row := EvalRow{Date: t.h.InternalDate, From: from, Actual: t.folder, Decision: d,
			NewSender: !known && !k.SentTo[from], Mixed: route.Mixed}
		row.Reply = t.h.InReplyTo != "" || len(t.h.References) > 0
		row.InMyThread = classify.InThread(t.h, k.SentIDs)
		if lr, ok := routeOf[from]; ok {
			row.RouteShare = lr.Shares[lr.Folder]
			for _, n := range lr.Counts {
				row.History += n
			}
		}
		rep.Rows = append(rep.Rows, row)

		agree := row.Agrees()
		rep.Overall.add(agree)
		if row.NewSender {
			rep.New.add(agree)
		} else {
			rep.Known.add(agree)
		}
		a := rep.ByRule[d.Rule]
		a.add(agree)
		rep.ByRule[d.Rule] = a
		if rep.Confusion[row.Actual] == nil {
			rep.Confusion[row.Actual] = map[string]int{}
		}
		rep.Confusion[row.Actual][d.Folder]++
		rk := RuleKey{d.Rule, d.Strength}
		st := rep.ByRuleStrength[rk]
		st.add(agree)
		if st.Actual == nil {
			st.Actual = map[string]int{}
		}
		st.Actual[row.Actual]++
		rep.ByRuleStrength[rk] = st

		actualInbox, predInbox := row.Actual == rep.Inbox, d.Folder == rep.Inbox
		rep.InboxAgreement.add(actualInbox == predInbox)
		switch {
		case agree:
		case actualInbox:
			rep.Harm.Hidden++
		case predInbox:
			rep.Harm.Leaked++
		default:
			rep.Harm.CrossFiled++
		}
		if actualInbox {
			rep.InboxTotal++
			if !predInbox {
				rep.HiddenImportant++
				rep.HiddenByRule[d.Rule]++
				if d.Rule == classify.RuleSender && row.Mixed {
					rep.HiddenSenderMixed++
				}
				if row.NewSender {
					rep.HiddenImportantNew++
				} else {
					rep.HiddenKnown.add(row)
				}
			}
		}
		if row.NewSender && (d.Rule == classify.RuleNewsletter || d.Rule == classify.RuleBulk || d.Rule == classify.RuleLater) {
			sigs := append([]string(nil), d.Signals...)
			sort.Strings(sigs)
			sigCounts[sigKey{d.Rule, d.Strength, row.Actual, strings.Join(sigs, "; ")}]++
		}
	}
	for k, n := range sigCounts {
		rep.Signals = append(rep.Signals, SignalCount{k.rule, k.strength, k.actual, k.signals, n})
	}
	sort.Slice(rep.Signals, func(i, j int) bool {
		a, c := rep.Signals[i], rep.Signals[j]
		switch {
		case a.Count != c.Count:
			return a.Count > c.Count
		case a.Rule != c.Rule:
			return a.Rule < c.Rule
		case a.Strength != c.Strength:
			return a.Strength > c.Strength
		case a.Actual != c.Actual:
			return a.Actual < c.Actual
		default:
			return a.Signals < c.Signals
		}
	})
	sort.SliceStable(rep.Rows, func(i, j int) bool {
		a, c := rep.Rows[i], rep.Rows[j]
		switch {
		case !a.Date.Equal(c.Date):
			return a.Date.After(c.Date)
		case a.From != c.From:
			return a.From < c.From
		default:
			return a.Actual < c.Actual
		}
	})
	return rep, nil
}

func (h *HiddenKnown) add(r EvalRow) {
	h.Total++
	if r.Reply {
		h.Replies++
	}
	if r.InMyThread {
		h.InMyThread++
	}
	if r.Mixed {
		h.Mixed++
	}
	switch {
	case r.RouteShare >= 0.9:
		h.ShareHigh++
	case r.RouteShare >= 0.6:
		h.ShareMid++
	default:
		h.ShareLow++
	}
	switch {
	case r.History >= 10:
		h.Hist10Plus++
	case r.History >= 3:
		h.Hist3to9++
	default:
		h.Hist1to2++
	}
}

// names lists the switched-on experiments in a fixed order.
func policyNames(p classify.Policy) []string {
	var out []string
	if p.ThreadFirst {
		out = append(out, "thread-first")
	}
	if p.NoMixedOverride {
		out = append(out, "no-mixed-override")
	}
	if p.OnlyStrong {
		out = append(out, "only-strong")
	}
	return out
}

func pct(a Agreement) string {
	return fmt.Sprintf("%d of %d (%.0f%%)", a.Agree, a.Total, a.Percent())
}

// Write renders the human report.
func (r *EvalReport) Write(w io.Writer) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Hold-out evaluation (nothing was changed): learned from %d messages before %s,\n",
		r.HistoryMessages, r.Cutoff.Format("2006-01-02"))
	fmt.Fprintf(&buf, "then classified the %d messages received on or after it. Skipped: %d from your own addresses, %d without a From.\n",
		r.Overall.Total, r.SkippedSelf, r.SkippedNoFrom)
	if names := policyNames(r.Policy); len(names) > 0 {
		fmt.Fprintf(&buf, "Experiment: %s (not the defaults)\n", strings.Join(names, ", "))
	}
	fmt.Fprintln(&buf)

	fmt.Fprintf(&buf, "  agreement, all senders    %s\n", pct(r.Overall))
	fmt.Fprintf(&buf, "  agreement, known senders  %s\n", pct(r.Known))
	fmt.Fprintf(&buf, "  agreement, new senders    %s   <- this measures the detectors\n\n", pct(r.New))

	disagree := r.Overall.Total - r.Overall.Agree
	fmt.Fprintf(&buf, "Disagreements: %d, by how much they hurt\n", disagree)
	fmt.Fprintf(&buf, "  hidden important  %4d  in %s, quietbox would file it away (the serious one)\n", r.Harm.Hidden, r.Inbox)
	fmt.Fprintf(&buf, "  leaked            %4d  filed, quietbox would leave it in %s (mild)\n", r.Harm.Leaked, r.Inbox)
	fmt.Fprintf(&buf, "  cross-filed       %4d  filed in a different folder, out of %s either way\n", r.Harm.CrossFiled, r.Inbox)
	fmt.Fprintf(&buf, "  inbox vs not      %s   (ignores which folder)\n\n", pct(r.InboxAgreement))

	fmt.Fprintf(&buf, "Hidden important: %d of %d messages in %s would have been filed elsewhere (%d from new senders)\n",
		r.HiddenImportant, r.InboxTotal, r.Inbox, r.HiddenImportantNew)
	if len(r.HiddenByRule) > 0 {
		var parts []string
		for _, rule := range sortedRules(r.HiddenByRule) {
			part := fmt.Sprintf("%s %d", rule, r.HiddenByRule[classify.Rule(rule)])
			if classify.Rule(rule) == classify.RuleSender && r.HiddenSenderMixed > 0 {
				part += fmt.Sprintf(" (%d from mixed senders)", r.HiddenSenderMixed)
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(&buf, "  by rule: %s\n", strings.Join(parts, ", "))
	}
	if hk := r.HiddenKnown; hk.Total > 0 {
		fmt.Fprintf(&buf, "  known senders (%d): replies %d (in a thread you started %d); route share >=90%% %d, 60-89%% %d, <60%% %d;\n"+
			"    history 1-2 msgs %d, 3-9 %d, 10+ %d; mixed senders %d\n",
			hk.Total, hk.Replies, hk.InMyThread, hk.ShareHigh, hk.ShareMid, hk.ShareLow,
			hk.Hist1to2, hk.Hist3to9, hk.Hist10Plus, hk.Mixed)
	}
	fmt.Fprintln(&buf)

	fmt.Fprintln(&buf, "Where mail is (rows) vs where quietbox would file it (columns):")
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	head := "ACTUAL"
	for _, f := range r.Folders {
		head += "\t" + f
	}
	_, _ = fmt.Fprintln(tw, head)
	for _, actual := range r.Folders {
		line := actual
		for _, pred := range r.Folders {
			line += "\t" + strconv.Itoa(r.Confusion[actual][pred])
		}
		_, _ = fmt.Fprintln(tw, line)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(&buf, "\nBy rule and strength (where the messages actually are):")
	keys := make([]RuleKey, 0, len(r.ByRuleStrength))
	for k := range r.ByRuleStrength {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Rule != keys[j].Rule {
			return keys[i].Rule < keys[j].Rule
		}
		return keys[i].Strength > keys[j].Strength
	})
	tw = tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  RULE\tSTRENGTH\tAGREE\tACTUAL FOLDER")
	for _, k := range keys {
		st := r.ByRuleStrength[k]
		var where []string
		for _, f := range r.Folders {
			if n := st.Actual[f]; n > 0 {
				where = append(where, fmt.Sprintf("%s:%d", f, n))
			}
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", k.Rule, k.Strength, pct(st.Agreement), strings.Join(where, " "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(r.Signals) > 0 {
		const maxSignalRows = 40
		fmt.Fprintln(&buf, "\nMost common new-sender detector decisions by signals (aggregate: no addresses or subjects):")
		tw = tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  COUNT\tRULE\tSTRENGTH\tACTUAL\tSIGNALS")
		for i, sc := range r.Signals {
			if i == maxSignalRows {
				_, _ = fmt.Fprintf(tw, "  ...\t%d more combinations\t\t\t\n", len(r.Signals)-maxSignalRows)
				break
			}
			_, _ = fmt.Fprintf(tw, "  %d\t%s\t%s\t%s\t%s\n", sc.Count, sc.Rule, sc.Strength, sc.Actual, sc.Signals)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// WriteCSV writes the disagreements only: date, from, actual, predicted, rule,
// new_sender, reason, signals. It has addresses but never subjects.
func (r *EvalReport) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"date", "from", "actual", "predicted", "rule", "new_sender", "reason", "signals"}); err != nil {
		return err
	}
	for _, row := range r.Rows {
		if row.Agrees() {
			continue
		}
		d := row.Decision
		rec := []string{row.Date.Format("2006-01-02"), row.From, row.Actual, d.Folder, string(d.Rule),
			strconv.FormatBool(row.NewSender), d.Reason, strings.Join(d.Signals, "; ")}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func sortedRules(m map[classify.Rule]int) []string {
	out := make([]string, 0, len(m))
	for r := range m {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return out
}
