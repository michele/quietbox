package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/learn"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// This file holds training from moves. The first half is pure: deciding, from
// data already loaded, which messages that appeared in a folder were moved
// there by the user (training events) and which quietbox moved itself (the
// audit log explains them). The second half wires it into a pass: reading the
// typed folders, applying events to the knowledge and persisting them.
// Training never touches the mailbox; it changes local state only.

// arrival is a message that appeared in a folder since its cursor.
type arrival struct {
	H        mail.Header // H.UID, H.InternalDate, H.From, H.MessageID set
	Key      string      // mail.Key(H)
	Folder   string      // folder it appeared in
	Validity uint32      // UIDVALIDITY of Folder when it was read
}

// trainEvent is one filing action by the user: Sender should go to Folder.
// Folder == the INBOX name means "keep in INBOX".
type trainEvent struct{ Sender, Folder, MessageKey string }

// destKey is where a message landed: folder, UIDVALIDITY and UID.
type destKey struct {
	folder   string
	validity uint32
	uid      uint32
}

// keyFolder is a message key in a folder, for audit rows without a destination UID.
type keyFolder struct{ key, folder string }

// ownMoves indexes the done mailbox moves quietbox made itself.
type ownMoves struct {
	dests     map[destKey]bool
	keyFolder map[keyFolder]bool   // rows with DestUID == 0
	moved     map[string]time.Time // latest done, not-undone move per key
	movedTo   map[string]string    // the folder of that move
}

// auditDone reports whether a row's move really happened ("" is stored as done).
func auditDone(r store.AuditRow) bool {
	return r.Status == store.AuditDone || r.Status == ""
}

func newOwnMoves(rows []store.AuditRow) *ownMoves {
	o := &ownMoves{
		dests:     make(map[destKey]bool),
		keyFolder: make(map[keyFolder]bool),
		moved:     make(map[string]time.Time),
		movedTo:   make(map[string]string),
	}
	for _, r := range rows {
		if (r.Action != store.ActionMove && r.Action != store.ActionUndoMove) || !auditDone(r) {
			continue
		}
		if r.DestUID != 0 {
			o.dests[destKey{r.ToFolder, r.DestUIDValidity, r.DestUID}] = true
		} else if !r.Undone {
			// An undone row no longer says where the message is, and without a
			// destination UID it would hide a later user move of the same key.
			o.keyFolder[keyFolder{r.MessageKey, r.ToFolder}] = true
		}
		if r.Action == store.ActionMove && !r.Undone {
			if at, ok := o.moved[r.MessageKey]; !ok || r.At.After(at) {
				o.moved[r.MessageKey] = r.At
				o.movedTo[r.MessageKey] = r.ToFolder
			}
		}
	}
	return o
}

// explains reports whether quietbox itself put a into its folder: a done
// move/undo_move row landed there with the same UIDVALIDITY and UID. Matching
// on the destination, not the key, keeps a message the user drags back and
// forth a user move. Rows without a destination UID match on key and folder.
func (o *ownMoves) explains(a arrival) bool {
	return o.dests[destKey{a.Folder, a.Validity, a.H.UID}] || o.keyFolder[keyFolder{a.Key, a.Folder}]
}

// movedAt is the time of the latest done, not-undone move of key.
func (o *ownMoves) movedAt(key string) (time.Time, bool) {
	at, ok := o.moved[key]
	return at, ok
}

// fileEvent turns a typed-folder arrival into a training event. It is false
// for mail from one of the user's own addresses or with no parseable From.
func fileEvent(a arrival, me map[string]bool) (trainEvent, bool) {
	if a.H.From == "" || me[a.H.From] {
		return trainEvent{}, false
	}
	return trainEvent{Sender: a.H.From, Folder: a.Folder, MessageKey: a.Key}, true
}

// returnVerdict says what an INBOX arrival is.
type returnVerdict int

const (
	notReturn       returnVerdict = iota // ordinary INBOX mail: classify it
	returnExplained                      // back in INBOX, but not a training event; never classify
	returnTrained                        // the user dragged it back: sender stays in INBOX
)

// inboxArrival decides whether an INBOX arrival is a message coming back from
// a typed folder. A move keeps INTERNALDATE and a fresh delivery does not, so
// a return needs a stored date that the arrival's INTERNALDATE is not after:
// the messages row when there is one, else the time of our own move. An
// arrival with no INTERNALDATE is never a return (fail safe).
func inboxArrival(a arrival, inbox string, known map[string]store.MessageRow, own *ownMoves, me map[string]bool) (returnVerdict, trainEvent) {
	isReturn := false
	if a.H.InternalDate.IsZero() {
		isReturn = false
	} else if row, ok := known[a.Key]; ok {
		isReturn = !a.H.InternalDate.After(row.InternalDate)
	} else if at, ok := own.movedAt(a.Key); ok {
		isReturn = !a.H.InternalDate.After(at)
	}
	if !isReturn {
		return notReturn, trainEvent{}
	}
	if own.explains(a) {
		return returnExplained, trainEvent{}
	}
	ev, ok := fileEvent(a, me)
	if !ok {
		return returnExplained, trainEvent{}
	}
	ev.Folder = inbox
	return returnTrained, ev
}

// Training statuses in a ProcessReport.
const (
	TrainDone  = "done"
	TrainWould = "would_train" // dry run
)

// TrainOutcome is one sender route a pass trained (or would train). From is
// the folder you moved the message out of when it is known (a return to
// INBOX), "" otherwise; Route is the sender's route before, "" if none. It
// holds addresses and folder names only.
type TrainOutcome struct{ Sender, From, Route, To, Source, Status string }

// String is the report line for the outcome.
func (t TrainOutcome) String() string {
	verb := "trained"
	if t.Status == TrainWould {
		verb = "would train"
	}
	return fmt.Sprintf("%s %s: %s", verb, t.Sender, movedText(t.From, t.Route, t.To))
}

// movedText says what you did: "you moved a message from A to B", or, when
// the origin is unknown, "you moved a message to B (route was R)".
func movedText(origin, route, to string) string {
	switch {
	case origin != "":
		return fmt.Sprintf("you moved a message from %s to %s", origin, to)
	case route != "":
		return fmt.Sprintf("you moved a message to %s (route was %s)", to, route)
	}
	return fmt.Sprintf("you moved a message to %s", to)
}

// trainReason is the stored reason of a trained route, shown by `explain`.
func trainReason(origin, route, to string, now time.Time) string {
	day := now.Format(time.DateOnly)
	switch {
	case origin != "":
		return fmt.Sprintf("you moved a message from %s to %s on %s", origin, to, day)
	case route != "":
		return fmt.Sprintf("route was %s; you moved a message to %s on %s", route, to, day)
	}
	return fmt.Sprintf("you moved a message to %s on %s", to, day)
}

// TrainedSenders is the number of distinct senders the pass trained.
func (r *ProcessReport) TrainedSenders() int {
	seen := map[string]bool{}
	for _, t := range r.Trained {
		seen[t.Sender] = true
	}
	return len(seen)
}

// passTraining is what one pass knows about training, shared by the typed
// folders and INBOX. It is kept current in memory in dry runs too, so a dry
// run decides exactly like the real run.
type passTraining struct {
	own    *ownMoves                   // our own moves, from the audit log
	known  map[string]store.MessageRow // where messages were last seen in typed folders
	last   map[string]string           // sender -> folder of its last event in this pass
	warned map[string]bool             // senders already warned about
}

// loadPassTraining reads the audit log and the remembered messages. A dry run
// does not reconcile, so it reads a move left `planned` by a crash as the
// reconciled row would mostly say (done, found by key in its destination):
// otherwise the dry run would report our own move as one of yours.
func loadPassTraining(ctx context.Context, st *store.Store, dryRun bool) (*passTraining, error) {
	audit, err := st.Audit(ctx)
	if err != nil {
		return nil, err
	}
	if dryRun {
		for i, r := range audit {
			if r.Action == store.ActionMove && r.Status == store.AuditPlanned {
				audit[i].Status, audit[i].DestUIDValidity, audit[i].DestUID = store.AuditDone, 0, 0
			}
		}
	}
	known, err := st.Messages(ctx)
	if err != nil {
		return nil, err
	}
	return &passTraining{own: newOwnMoves(audit), known: known, last: map[string]string{}, warned: map[string]bool{}}, nil
}

// trainedRoute is an applied training event, ready to persist.
type trainedRoute struct {
	Sender, Folder, MessageKey, Reason string
	At                                 time.Time
}

// pendingEvent is a training event and the folder the message was moved out
// of, when known ("" for typed-folder arrivals: arrivals carry no origin).
type pendingEvent struct {
	trainEvent
	Origin string
}

// applyTraining applies events in order to the knowledge k (later wins per
// sender) and returns what a real run must persist. An event that matches the
// sender's current trained route changes nothing. It does no I/O, so the
// caller can persist the result in the same transaction as the cursor.
func applyTraining(k *classify.Knowledge, pt *passTraining, events []pendingEvent, source string, o ProcessOptions, rep *ProcessReport) []trainedRoute {
	var out []trainedRoute
	for _, e := range events {
		if prev, ok := pt.last[e.Sender]; ok && prev != e.Folder && !pt.warned[e.Sender] {
			pt.warned[e.Sender] = true
			rep.Notes = append(rep.Notes, fmt.Sprintf("moves for %s disagreed in one pass; the last one won", e.Sender))
		}
		pt.last[e.Sender] = e.Folder
		cur, had := k.Senders[e.Sender]
		if had && cur.Trained && !cur.Mixed && cur.Folder == e.Folder {
			continue
		}
		route := ""
		if had {
			route = cur.Folder
		}
		reason := trainReason(e.Origin, route, e.Folder, o.Now)
		k.Senders[e.Sender] = classify.SenderRoute{Folder: e.Folder, Trained: true, Reason: reason}
		status := TrainDone
		if o.DryRun {
			status = TrainWould
		}
		rep.Trained = append(rep.Trained, TrainOutcome{Sender: e.Sender, From: e.Origin, Route: route, To: e.Folder, Source: source, Status: status})
		out = append(out, trainedRoute{Sender: e.Sender, Folder: e.Folder, MessageKey: e.MessageKey, Reason: reason, At: o.Now})
	}
	return out
}

// recordTraining persists applied events: the sender's trained route and one
// training row per event. Replaying an event after an interrupted pass (same
// sender, folder and message as the sender's newest training row) adds no
// second row.
func recordTraining(tx *store.Tx, routes []trainedRoute, source string) error {
	for _, r := range routes {
		if err := tx.UpsertTrainedSender(store.SenderRow{
			Address: r.Sender, Domain: learn.DomainOf(r.Sender), RouteFolder: r.Folder, Reason: r.Reason, UpdatedAt: r.At,
		}); err != nil {
			return err
		}
		latest, ok, err := tx.LatestTraining(r.Sender)
		if err != nil {
			return err
		}
		if ok && latest.Folder == r.Folder && latest.MessageKey == r.MessageKey {
			continue
		}
		if err := tx.AddTraining(store.TrainingRow{At: r.At, Sender: r.Sender, Folder: r.Folder, Source: source, MessageKey: r.MessageKey}); err != nil {
			return err
		}
	}
	return nil
}

// newArrival is a fetched message as an arrival in folder. The key is taken
// from the header as fetched; From is compared lower-case.
func newArrival(h mail.Header, folder string, validity uint32) arrival {
	key := mail.Key(h)
	h.From = strings.ToLower(h.From)
	return arrival{H: h, Key: key, Folder: folder, Validity: validity}
}

// processTyped reads what arrived in each configured typed folder (every
// folder but INBOX and Sent that exists on the server), in config order. Each
// arrival is remembered in `messages`; one the audit log does not explain is a
// file event for its sender. A folder's events, messages and cursor are
// written in one transaction.
func processTyped(ctx context.Context, cfg *config.Config, b mail.Backend, st *store.Store, o ProcessOptions, k *classify.Knowledge, pt *passTraining, rep *ProcessReport) error {
	server, err := b.ListFolders(ctx)
	if err != nil {
		return err
	}
	sentName, err := sentFolder(cfg, server)
	if err != nil {
		return refuse(err)
	}
	onServer := map[string]bool{}
	for _, f := range server {
		onServer[f.Name] = true
	}
	var started []string // folders that got their first cursor in this pass
	for _, cf := range cfg.Folders {
		if cf.Name == k.Inbox || cf.Name == sentName || !onServer[cf.Name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := processTypedFolder(ctx, b, st, o, k, pt, cf.Name, &started, rep); err != nil {
			return err
		}
	}
	if len(started) > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("training from moves starts now for %s; earlier moves are not learned", strings.Join(started, ", ")))
	}
	return nil
}

func processTypedFolder(ctx context.Context, b mail.Backend, st *store.Store, o ProcessOptions, k *classify.Knowledge, pt *passTraining, folder string, started *[]string, rep *ProcessReport) error {
	if ok, err := ensureCursor(ctx, b, st, o, folder, started); err != nil || !ok {
		return err
	}
	cur, status, ok, err := checkCursor(ctx, b, st, o, folder, "nothing was learned from it", rep)
	if err != nil || !ok {
		return err
	}
	var arrivals []arrival
	if err := b.FetchFrom(ctx, folder, cur.NextUID, func(hs []mail.Header) error {
		for _, h := range hs {
			arrivals = append(arrivals, newArrival(h, folder, status.UIDValidity))
		}
		return nil
	}); err != nil {
		return fmt.Errorf("fetch new mail from %s: %w", folder, err)
	}
	maxUID := cur.NextUID - 1
	var rows []store.MessageRow
	var events []pendingEvent
	for _, a := range arrivals {
		maxUID = max(maxUID, a.H.UID)
		row := store.MessageRow{Key: a.Key, Folder: folder, InternalDate: a.H.InternalDate}
		rows = append(rows, row)
		pt.known[a.Key] = row
		if pt.own.explains(a) {
			continue // quietbox put it there
		}
		if ev, ok := fileEvent(a, k.Me); ok {
			events = append(events, pendingEvent{trainEvent: ev})
		}
	}
	progress(o, "%s: %d new message(s), %d moved there by you", folder, len(arrivals), len(events))
	routes := applyTraining(k, pt, events, store.TrainMove, o, rep)
	if o.DryRun {
		return nil
	}
	updated := cur
	updated.NextUID = max(cur.NextUID, maxUID+1, status.UIDNext)
	if len(rows) == 0 && updated == cur {
		return nil
	}
	return st.Update(ctx, func(tx *store.Tx) error {
		if err := tx.PutMessages(rows); err != nil {
			return err
		}
		if err := recordTraining(tx, routes, store.TrainMove); err != nil {
			return err
		}
		return tx.SetCursor(updated)
	})
}

// ensureCursor reports whether a typed folder has a cursor. A database from
// before M4 has none: a real run starts one at the folder's current end (moves
// made before now are not learned, and the folder is added to started); a dry
// run skips the folder.
func ensureCursor(ctx context.Context, b mail.Backend, st *store.Store, o ProcessOptions, folder string, started *[]string) (bool, error) {
	if _, found, err := st.Cursor(ctx, folder); err != nil || found {
		return found, err
	}
	if o.DryRun {
		return false, nil
	}
	fs, err := b.FolderStatus(ctx, folder)
	if err != nil {
		return false, fmt.Errorf("status of %s: %w", folder, err)
	}
	if err := st.SetCursor(ctx, store.Cursor{Folder: folder, UIDValidity: fs.UIDValidity, NextUID: fs.UIDNext}); err != nil {
		return false, err
	}
	*started = append(*started, folder)
	return false, nil
}
