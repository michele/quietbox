// Package stats turns the audit log and the training events into the numbers
// `quietbox stats` prints. It is pure: no database, no IMAP, no files.
package stats

import (
	"sort"
	"strings"
	"time"
)

// Move is one `move` audit row.
type Move struct {
	Key, From, To, Reason string // Reason is stored as "<rule>: <text>"
	At                    time.Time
	Done, Undone          bool // Done: the audit status is "done"
}

// Event is one training event.
type Event struct {
	At             time.Time
	Sender, Folder string // Folder is the new route; Sender is only used to count retrained senders
	Key            string
	ByMove         bool // false for `senders set` (manual)
}

// Input is everything Compute needs.
type Input struct {
	Moves             []Move
	Events            []Event
	Now               time.Time
	Window            time.Duration
	SecondsPerMessage int
	Inbox             string   // name of the INBOX folder
	FolderOrder       []string // config order; unknown folders sort after, alphabetically
	RetentionRule     string   // rule name of retention purges
}

// RuleCount is how many messages one rule filed into a folder.
type RuleCount struct {
	Rule  string `json:"rule"`
	Count int    `json:"count"`
}

// FolderStat is the mail filed into one folder.
type FolderStat struct {
	Folder string      `json:"folder"`
	Count  int         `json:"count"`
	Share  float64     `json:"share"`
	Purged int         `json:"purged"` // retention purges from this folder to Trash
	Rules  []RuleCount `json:"rules"`  // count descending, then name
}

// Report is the result of Compute.
type Report struct {
	Since             time.Time    `json:"since"`
	Until             time.Time    `json:"until"`
	Filed             int          `json:"filed"`
	SavedSeconds      int          `json:"saved_seconds"`
	SecondsPerMessage int          `json:"seconds_per_message"` // echoed from Input, for the "(N s each)" label
	Folders           []FolderStat `json:"folders"`
	Corrected         int          `json:"corrected"`
	BackToInbox       int          `json:"back_to_inbox"`
	Refiled           int          `json:"refiled"`
	Undone            int          `json:"undone"`
	Retrained         int          `json:"retrained"`
	ErrorRate         float64      `json:"error_rate"` // Corrected / Filed; 0 when nothing was filed
}

// ruleOf is the text before the first ": " of an audit reason.
func ruleOf(reason string) string {
	if i := strings.Index(reason, ": "); i > 0 {
		return reason[:i]
	}
	return "other"
}

// Compute builds the report for the window ending at in.Now.
func Compute(in Input) Report {
	since := in.Now.Add(-in.Window)
	r := Report{Since: since, Until: in.Now, SecondsPerMessage: in.SecondsPerMessage}

	type acc struct {
		count, purged int
		rules         map[string]int
	}
	byFolder := map[string]*acc{}
	get := func(f string) *acc {
		if byFolder[f] == nil {
			byFolder[f] = &acc{rules: map[string]int{}}
		}
		return byFolder[f]
	}
	filed := map[string]Move{} // by key: filed (not purged), not undone
	for _, m := range in.Moves {
		if !m.Done || m.At.Before(since) {
			continue
		}
		if m.Undone {
			r.Undone++
		}
		rule := ruleOf(m.Reason)
		if rule == in.RetentionRule {
			get(m.From).purged++
			continue
		}
		r.Filed++
		a := get(m.To)
		a.count++
		a.rules[rule]++
		if !m.Undone {
			if _, dup := filed[m.Key]; !dup {
				filed[m.Key] = m
			}
		}
	}
	r.SavedSeconds = r.Filed * in.SecondsPerMessage

	// A correction is a move by you of a message quietbox filed, after it was
	// filed. One message counts once; its latest event decides the kind.
	latest := map[string]Event{}
	senders := map[string]struct{}{}
	for _, e := range in.Events {
		if e.At.Before(since) {
			continue
		}
		senders[e.Sender] = struct{}{}
		m, ok := filed[e.Key]
		if !e.ByMove || !ok || e.At.Before(m.At) {
			continue
		}
		if prev, seen := latest[e.Key]; !seen || !e.At.Before(prev.At) {
			latest[e.Key] = e
		}
	}
	r.Retrained = len(senders)
	for _, e := range latest {
		r.Corrected++
		if e.Folder == in.Inbox {
			r.BackToInbox++
		} else {
			r.Refiled++
		}
	}
	if r.Filed > 0 {
		r.ErrorRate = float64(r.Corrected) / float64(r.Filed)
	}

	rank := map[string]int{}
	for i, f := range in.FolderOrder {
		rank[f] = i
	}
	names := make([]string, 0, len(byFolder))
	for f := range byFolder {
		names = append(names, f)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, iok := rank[names[i]]
		rj, jok := rank[names[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return names[i] < names[j]
	})
	for _, f := range names {
		a := byFolder[f]
		fs := FolderStat{Folder: f, Count: a.count, Purged: a.purged}
		if r.Filed > 0 {
			fs.Share = float64(a.count) / float64(r.Filed)
		}
		for rule, n := range a.rules {
			fs.Rules = append(fs.Rules, RuleCount{Rule: rule, Count: n})
		}
		sort.Slice(fs.Rules, func(i, j int) bool {
			if fs.Rules[i].Count != fs.Rules[j].Count {
				return fs.Rules[i].Count > fs.Rules[j].Count
			}
			return fs.Rules[i].Rule < fs.Rules[j].Rule
		})
		r.Folders = append(r.Folders, fs)
	}
	return r
}
