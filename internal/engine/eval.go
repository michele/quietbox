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
	History  time.Duration
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
	}

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
	for _, r := range routes {
		k.Senders[r.Address] = classify.SenderRoute{Folder: r.Folder, Mixed: r.Mixed, Reason: r.Reason}
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
		_, known := k.Senders[from]
		row := EvalRow{Date: t.h.InternalDate, From: from, Actual: t.folder, Decision: d,
			NewSender: !known && !k.SentTo[from]}
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
		if row.Actual == rep.Inbox {
			rep.InboxTotal++
			if d.Folder != rep.Inbox {
				rep.HiddenImportant++
				if row.NewSender {
					rep.HiddenImportantNew++
				}
			}
		}
	}
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

func pct(a Agreement) string {
	return fmt.Sprintf("%d of %d (%.0f%%)", a.Agree, a.Total, a.Percent())
}

// Write renders the human report.
func (r *EvalReport) Write(w io.Writer) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Hold-out evaluation (nothing was changed): learned from %d messages before %s,\n",
		r.HistoryMessages, r.Cutoff.Format("2006-01-02"))
	fmt.Fprintf(&buf, "then classified the %d messages received on or after it. Skipped: %d from your own addresses, %d without a From.\n\n",
		r.Overall.Total, r.SkippedSelf, r.SkippedNoFrom)

	fmt.Fprintf(&buf, "  agreement, all senders    %s\n", pct(r.Overall))
	fmt.Fprintf(&buf, "  agreement, known senders  %s\n", pct(r.Known))
	fmt.Fprintf(&buf, "  agreement, new senders    %s   <- this measures the detectors\n\n", pct(r.New))

	fmt.Fprintf(&buf, "Hidden important: %d of %d messages in %s would have been filed elsewhere (%d from new senders)\n\n",
		r.HiddenImportant, r.InboxTotal, r.Inbox, r.HiddenImportantNew)

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

	fmt.Fprintln(&buf, "\nBy rule:")
	rules := make([]string, 0, len(r.ByRule))
	for rule := range r.ByRule {
		rules = append(rules, string(rule))
	}
	sort.Strings(rules)
	tw = tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  RULE\tAGREE")
	for _, rule := range rules {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\n", rule, pct(r.ByRule[classify.Rule(rule)]))
	}
	if err := tw.Flush(); err != nil {
		return err
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
