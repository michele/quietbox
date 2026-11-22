package engine

import (
	"time"

	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// This file holds the blackhole retention sweep: mail that has sat in the
// blackhole folder longer than the retention period is moved to Trash (never
// deleted). The age counts from when the message was first seen in the folder,
// because a move keeps INTERNALDATE and a message blocked late would otherwise
// be trashed almost at once. The first half is pure; the second half does the
// I/O.

// sweepItem is one message the sweep will move.
type sweepItem struct {
	UID uint32
	Key string
}

// sweepPlan is what planSweep decides.
type sweepPlan struct {
	Due         []sweepItem              // messages past retention, in folder order
	Seen        map[string]store.SeenRow // the first-seen rows to store: exactly the messages now in the folder
	KeptFlagged int                      // past retention but flagged
	KeptRescued int                      // past retention but restored by undo
}

// planSweep decides which messages of the blackhole folder (hs) are due. seen
// holds the first-seen times from earlier sweeps; a message not in it is seen
// now for the first time and is never due yet. A flagged message, and one whose
// key is in rescued (a purge you reversed with undo), is never due. Messages
// that are no longer in the folder drop out of the returned Seen. It does no I/O.
func planSweep(hs []mail.Header, seen map[string]store.SeenRow, rescued map[string]bool, now time.Time, retention time.Duration) sweepPlan {
	p := sweepPlan{Seen: make(map[string]store.SeenRow, len(hs))}
	for _, h := range hs {
		key := mail.Key(h)
		row, ok := p.Seen[key] // a second copy of the same message shares the row
		if !ok {
			if row, ok = seen[key]; !ok {
				row = store.SeenRow{FirstSeen: now}
			}
			row.Key = key // the map key is what counts
			p.Seen[key] = row
		}
		if now.Sub(row.FirstSeen) < retention {
			continue
		}
		switch {
		case h.HasFlag(`\Flagged`):
			p.KeptFlagged++
		case rescued[key]:
			p.KeptRescued++
		default:
			p.Due = append(p.Due, sweepItem{UID: h.UID, Key: key})
		}
	}
	return p
}
