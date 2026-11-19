package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/learn"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// LearnOptions controls Learn.
type LearnOptions struct {
	Now    time.Time     // "as of"; zero means time.Now()
	Since  time.Duration // limit scan depth; 0 = no limit (INBOX still has its own lookback)
	Folder string        // re-learn only this configured folder; "" = all
	DryRun bool          // no mailbox mutation and no database writes
	Yes    bool          // create missing folders without asking
	// Confirm is asked before creating each missing folder when Yes is false.
	// With neither, missing folders are skipped.
	Confirm  func(folder string) (bool, error)
	Progress func(msg string) // optional progress lines
}

// FolderScan is one configured folder as seen by Learn.
type FolderScan struct {
	Name    string
	Type    config.FolderType
	Present bool
	Scanned int // unique messages read
}

// SenderStat is the raw per-folder history behind a route.
type SenderStat struct {
	Counts   map[string]int
	LastSeen time.Time
}

// LearnReport is everything Learn found and decided.
type LearnReport struct {
	Now    time.Time
	DryRun bool
	Scope  string

	Folders       []FolderScan
	Created       []string
	WouldCreate   []string
	SkippedCreate []string

	Senders []learn.Route // only senders with a learned route, sorted by address
	Stats   map[string]SenderStat
	Domains []learn.DomainRoute

	Scanned        int // unique messages read from configured folders
	SentMessages   int
	SentRecipients int
	SkippedNoFrom  int
	SkippedSelf    int
}

// Learn scans the configured folders and Sent, resolves a route per sender and
// domain defaults, and (unless DryRun) stores the result. The only mailbox
// mutation is creating missing configured folders, each audited first.
// Nothing is written until every scan has succeeded.
func Learn(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o LearnOptions) (*LearnReport, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	progress := func(format string, a ...any) {
		if o.Progress != nil {
			o.Progress(fmt.Sprintf(format, a...))
		}
	}
	scoped := o.Folder != ""
	switch {
	case !o.DryRun && st == nil:
		return nil, errors.New("learn: a database is required unless --dry-run")
	case scoped && st == nil:
		return nil, errors.New("re-learning one folder needs an existing database: run a full `quietbox init` first")
	}
	if scoped {
		if !isConfigured(cfg, o.Folder) {
			return nil, fmt.Errorf("--folder %q is not a configured folder", o.Folder)
		}
		if known, err := st.Folders(ctx); err != nil {
			return nil, err
		} else if len(known) == 0 {
			return nil, errors.New("re-learning one folder needs a previous full run: run `quietbox init` first")
		}
	}
	inScope := func(name string) bool { return !scoped || name == o.Folder }

	server, err := b.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	onServer := map[string]bool{"INBOX": true}
	for _, f := range server {
		onServer[f.Name] = true
	}

	rep := &LearnReport{Now: o.Now, DryRun: o.DryRun, Scope: o.Folder, Stats: map[string]SenderStat{}}

	// 1. Create missing folders (the only mutation), audit row first.
	for _, cf := range cfg.Folders {
		if onServer[cf.Name] || !inScope(cf.Name) {
			continue
		}
		switch {
		case o.DryRun:
			rep.WouldCreate = append(rep.WouldCreate, cf.Name)
			continue
		case !o.Yes:
			ok := false
			if o.Confirm != nil {
				if ok, err = o.Confirm(cf.Name); err != nil {
					return nil, err
				}
			}
			if !ok {
				rep.SkippedCreate = append(rep.SkippedCreate, cf.Name)
				continue
			}
		}
		if _, err := st.AddAudit(ctx, store.AuditRow{
			Action: "create_folder", ToFolder: cf.Name, Reason: "configured folder does not exist", At: time.Now(),
		}); err != nil {
			return nil, fmt.Errorf("write audit log: %w", err)
		}
		if err := b.CreateFolder(ctx, cf.Name); err != nil {
			return nil, err
		}
		onServer[cf.Name] = true
		rep.Created = append(rep.Created, cf.Name)
	}

	// 2. Scan.
	me := map[string]bool{}
	for _, a := range cfg.Me.Addresses {
		me[a] = true
	}

	var sentName string
	if !scoped {
		if sentName, err = sentFolder(cfg, server); err != nil {
			return nil, err
		}
	}

	var newObs []store.Obs
	scanned := map[string]int{}
	for _, cf := range cfg.Folders {
		if !inScope(cf.Name) || !onServer[cf.Name] || cf.Name == sentName {
			continue
		}
		since := scanSince(cfg, cf, o)
		progress("scanning %s ...", cf.Name)
		seen := map[string]struct{}{}
		err := b.FetchHeaders(ctx, cf.Name, since, func(batch []mail.Header) error {
			for _, h := range batch {
				k := mail.Key(h)
				if _, dup := seen[k]; dup {
					continue // same message twice in one folder counts once
				}
				seen[k] = struct{}{}
				from := strings.ToLower(h.From)
				switch {
				case from == "":
					rep.SkippedNoFrom++
				case me[from]:
					rep.SkippedSelf++
				default:
					newObs = append(newObs, store.Obs{Address: from, Folder: cf.Name, At: h.InternalDate})
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", cf.Name, err)
		}
		scanned[cf.Name] = len(seen)
		rep.Scanned += len(seen)
		progress("  %s: %d messages", cf.Name, len(seen))
	}

	var sentRows []store.SentRow
	var sentIDs []store.SentMsg
	if scoped {
		if sentRows, err = st.SentTo(ctx); err != nil {
			return nil, err
		}
	} else {
		progress("scanning %s (sent mail) ...", sentName)
		sent := map[string]*store.SentRow{}
		seen := map[string]struct{}{}
		var since time.Time
		if o.Since > 0 {
			since = o.Now.Add(-o.Since)
		}
		err := b.FetchHeaders(ctx, sentName, since, func(batch []mail.Header) error {
			for _, h := range batch {
				k := mail.Key(h)
				if _, dup := seen[k]; dup {
					continue
				}
				seen[k] = struct{}{}
				when := h.Date
				if when.IsZero() {
					when = h.InternalDate
				}
				if h.MessageID != "" {
					sentIDs = append(sentIDs, store.SentMsg{MessageID: h.MessageID, At: when})
				}
				once := map[string]bool{}
				for _, to := range h.To {
					to = strings.ToLower(to)
					if to == "" || me[to] || once[to] {
						continue
					}
					once[to] = true
					row := sent[to]
					if row == nil {
						row = &store.SentRow{Address: to}
						sent[to] = row
					}
					row.Count++
					if when.After(row.LastSent) {
						row.LastSent = when
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", sentName, err)
		}
		rep.SentMessages = len(seen)
		for _, row := range sent {
			sentRows = append(sentRows, *row)
		}
		sort.Slice(sentRows, func(i, j int) bool { return sentRows[i].Address < sentRows[j].Address })
		progress("  %s: %d messages to %d recipients", sentName, rep.SentMessages, len(sentRows))
	}
	rep.SentRecipients = len(sentRows)

	// 3. Resolve routes (merging the stored rest of the mailbox when scoped).
	allObs := newObs
	if scoped {
		old, err := st.Observations(ctx)
		if err != nil {
			return nil, err
		}
		for _, ob := range old {
			if ob.Folder != o.Folder {
				allObs = append(allObs, ob)
			}
		}
	}
	p := learnParams(cfg)
	byAddr := map[string][]learn.Observation{}
	for _, ob := range allObs {
		byAddr[ob.Address] = append(byAddr[ob.Address], learn.Observation{Folder: ob.Folder, At: ob.At})
		s := rep.Stats[ob.Address]
		if s.Counts == nil {
			s.Counts = map[string]int{}
		}
		s.Counts[ob.Folder]++
		if ob.At.After(s.LastSeen) {
			s.LastSeen = ob.At
		}
		rep.Stats[ob.Address] = s
	}
	sentBy := map[string]int{}
	for _, row := range sentRows {
		sentBy[row.Address] = row.Count
		if _, ok := byAddr[row.Address]; !ok {
			byAddr[row.Address] = nil
			rep.Stats[row.Address] = SenderStat{Counts: map[string]int{}, LastSeen: row.LastSent}
		}
	}
	addrs := make([]string, 0, len(byAddr))
	for a := range byAddr {
		addrs = append(addrs, a)
	}
	sort.Strings(addrs)
	for _, a := range addrs {
		r := learn.Resolve(learn.SenderInput{Address: a, Obs: byAddr[a], SentTo: sentBy[a]}, p, o.Now)
		if r.Folder != "" {
			rep.Senders = append(rep.Senders, r)
		}
	}
	rep.Domains = learn.DomainDefaults(rep.Senders, p)

	for _, cf := range cfg.Folders {
		rep.Folders = append(rep.Folders, FolderScan{
			Name: cf.Name, Type: cf.Type, Present: onServer[cf.Name], Scanned: scanned[cf.Name],
		})
	}

	// 4. Persist everything in one transaction, only now that all scans worked.
	if o.DryRun {
		return rep, nil
	}
	senderRows := make([]store.SenderRow, 0, len(rep.Senders))
	for _, r := range rep.Senders {
		senderRows = append(senderRows, store.SenderRow{
			Address: r.Address, Domain: r.Domain, RouteFolder: r.Folder, RouteSource: store.SourceLearned,
			Mixed: r.Mixed, SentTo: r.SentTo, Reason: r.Reason, UpdatedAt: o.Now,
		})
	}
	domainRows := make([]store.DomainRow, 0, len(rep.Domains))
	for _, d := range rep.Domains {
		domainRows = append(domainRows, store.DomainRow{Domain: d.Domain, Folder: d.Folder, Senders: d.Senders, Agreeing: d.Agreeing})
	}
	err = st.Update(ctx, func(tx *store.Tx) error {
		for _, cf := range cfg.Folders {
			if !inScope(cf.Name) {
				continue
			}
			row := store.FolderRow{Name: cf.Name, Type: string(cf.Type), Present: onServer[cf.Name]}
			if onServer[cf.Name] && cf.Name != sentName {
				row.LastScanned = o.Now
			}
			if err := tx.UpsertFolder(row); err != nil {
				return err
			}
		}
		var scope []string
		if scoped {
			scope = []string{o.Folder}
		}
		if err := tx.ReplaceObservations(scope, newObs); err != nil {
			return err
		}
		if !scoped {
			if err := tx.ReplaceSentTo(sentRows); err != nil {
				return err
			}
			if err := tx.ReplaceSentMessageIDs(sentIDs); err != nil {
				return err
			}
		}
		if err := tx.ReplaceSenders(senderRows); err != nil {
			return err
		}
		return tx.ReplaceDomains(domainRows)
	})
	if err != nil {
		return nil, fmt.Errorf("save learned routes: %w", err)
	}
	return rep, nil
}

func isConfigured(cfg *config.Config, name string) bool {
	for _, f := range cfg.Folders {
		if f.Name == name {
			return true
		}
	}
	return false
}

// scanSince is how far back to read a folder: --since for everything, and the
// inbox lookback for INBOX (old inbox mail may predate other filtering tools).
func scanSince(cfg *config.Config, cf config.Folder, o LearnOptions) time.Time {
	var since time.Time
	if o.Since > 0 {
		since = o.Now.Add(-o.Since)
	}
	if cf.Type == config.TypeImportant {
		lookback := o.Now.Add(-cfg.Learn.InboxLookbackDur)
		if since.IsZero() || lookback.After(since) {
			since = lookback
		}
	}
	return since
}

// sentFolder finds the folder holding sent mail: learn.sent_folder if set,
// otherwise the one the server marks \Sent (SPECIAL-USE).
func sentFolder(cfg *config.Config, server []mail.Folder) (string, error) {
	if want := cfg.Learn.SentFolder; want != "" {
		for _, f := range server {
			if f.Name == want {
				return want, nil
			}
		}
		return "", fmt.Errorf("learn.sent_folder %q does not exist on the server", want)
	}
	for _, f := range server {
		for _, a := range f.Attrs {
			if strings.EqualFold(a, `\Sent`) {
				return f.Name, nil
			}
		}
	}
	return "", errors.New(`no Sent folder found (the server does not mark one with \Sent): set learn.sent_folder in the config`)
}

func learnParams(cfg *config.Config) learn.Params {
	p := learn.Params{
		Window:            cfg.Learn.Window,
		HalfLife:          cfg.Learn.HalfLifeDur,
		MixedShare:        cfg.Learn.MixedShare,
		SentOverrideShare: cfg.Learn.SentOverrideShare,
		DomainMinSenders:  cfg.Learn.DomainMinSenders,
		DomainShare:       cfg.Learn.DomainShare,
		PersonalDomains:   cfg.Learn.PersonalDomains,
	}
	for _, f := range cfg.Folders {
		switch f.Type {
		case config.TypeImportant:
			p.InboxFolder = f.Name
		case config.TypeBlackhole:
			p.BlackholeFolder = f.Name
		}
	}
	return p
}
