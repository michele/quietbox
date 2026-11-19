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

// ScanOptions controls Scan.
type ScanOptions struct {
	Now      time.Time     // "as of"; zero means time.Now()
	Since    time.Duration // how far back to look; 0 = learn.inbox_lookback
	Progress func(msg string)
}

// ScanRow is one INBOX message and what quietbox would do with it. It holds no
// subject and no body.
type ScanRow struct {
	Date     time.Time // server arrival date
	From     string
	Decision classify.Decision
}

// WouldMove reports whether the decision files the message somewhere else.
func (r ScanRow) wouldMove(inbox string) bool { return r.Decision.Folder != inbox }

// ScanReport is the shadow-mode result: nothing in the mailbox was changed.
type ScanReport struct {
	Now      time.Time
	Since    time.Time
	Inbox    string
	Rows     []ScanRow // newest first
	ByFolder map[string]int
	ByRule   map[classify.Rule]int
}

// Moves counts the messages that would leave INBOX.
func (r *ScanReport) Moves() int {
	n := 0
	for _, row := range r.Rows {
		if row.wouldMove(r.Inbox) {
			n++
		}
	}
	return n
}

// Scan classifies INBOX mail with what `quietbox init` learned and reports what
// it would do. It is strictly read-only: the mailbox is opened read-only and
// nothing is written to the database or the audit log.
func Scan(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ScanOptions) (*ScanReport, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if st == nil {
		return nil, store.ErrNotInitialised
	}
	if known, err := st.Folders(ctx); err != nil {
		return nil, err
	} else if len(known) == 0 {
		return nil, errors.New("the database holds nothing learned yet: run `quietbox init` first")
	}
	k, targets, err := KnowledgeFromStore(ctx, cfg, st)
	if err != nil {
		return nil, err
	}

	lookback := cfg.Learn.InboxLookbackDur
	if o.Since > 0 {
		lookback = o.Since
	}
	since := o.Now.Add(-lookback)
	rep := &ScanReport{
		Now: o.Now, Since: since, Inbox: k.Inbox,
		ByFolder: map[string]int{}, ByRule: map[classify.Rule]int{},
	}
	if o.Progress != nil {
		o.Progress(fmt.Sprintf("scanning %s since %s ...", k.Inbox, since.Format("2006-01-02")))
	}

	seen := map[string]struct{}{}
	err = b.FetchHeaders(ctx, k.Inbox, since, func(batch []mail.Header) error {
		for _, h := range batch {
			key := mail.Key(h)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			d := classify.Classify(h, k, targets)
			rep.Rows = append(rep.Rows, ScanRow{Date: h.InternalDate, From: strings.ToLower(h.From), Decision: d})
			rep.ByFolder[d.Folder]++
			rep.ByRule[d.Rule]++
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", k.Inbox, err)
	}
	sort.SliceStable(rep.Rows, func(i, j int) bool {
		a, c := rep.Rows[i], rep.Rows[j]
		switch {
		case !a.Date.Equal(c.Date):
			return a.Date.After(c.Date)
		case a.From != c.From:
			return a.From < c.From
		default:
			return a.Decision.Folder < c.Decision.Folder
		}
	})
	return rep, nil
}

// Write renders the human report. By default it lists only the messages that
// would move; all includes the ones that stay. limit caps the listing (0 = no cap).
func (r *ScanReport) Write(w io.Writer, limit int, all bool) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%s shadow scan: %d messages since %s, %d would move (nothing was changed)\n\n",
		r.Inbox, len(r.Rows), r.Since.Format("2006-01-02"), r.Moves())

	var listed []ScanRow
	for _, row := range r.Rows {
		if all || row.wouldMove(r.Inbox) {
			listed = append(listed, row)
		}
	}
	hidden := 0
	if limit > 0 && len(listed) > limit {
		hidden = len(listed) - limit
		listed = listed[:limit]
	}
	if len(listed) > 0 {
		tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "DATE\tFROM\tFOLDER\tRULE\tWHY")
		for _, row := range listed {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", row.Date.Format("2006-01-02"), row.From,
				row.Decision.Folder, row.Decision.Rule, row.Decision.Reason)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	if hidden > 0 {
		fmt.Fprintf(&buf, "... and %d more (raise --limit, use --limit 0, or write --csv for all of them)\n", hidden)
	}

	fmt.Fprintln(&buf, "\nBy folder:")
	for _, f := range sortedKeys(r.ByFolder) {
		fmt.Fprintf(&buf, "  %-20s %d\n", f, r.ByFolder[f])
	}
	fmt.Fprintln(&buf, "By rule:")
	rules := make([]string, 0, len(r.ByRule))
	byRule := map[string]int{}
	for rule, n := range r.ByRule {
		rules = append(rules, string(rule))
		byRule[string(rule)] = n
	}
	sort.Strings(rules)
	for _, rule := range rules {
		fmt.Fprintf(&buf, "  %-20s %d\n", rule, byRule[rule])
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// WriteCSV writes every row: date, from, folder, rule, would_move, reason, signals.
// It has addresses but never subjects or bodies.
func (r *ScanReport) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"date", "from", "folder", "rule", "would_move", "reason", "signals"}); err != nil {
		return err
	}
	for _, row := range r.Rows {
		d := row.Decision
		rec := []string{
			row.Date.Format("2006-01-02"), row.From, d.Folder, string(d.Rule),
			strconv.FormatBool(row.wouldMove(r.Inbox)), d.Reason, strings.Join(d.Signals, "; "),
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
