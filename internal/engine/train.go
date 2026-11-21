package engine

import (
	"time"

	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

// This file holds the pure half of training: deciding, from data already
// loaded, which messages that appeared in a folder were moved there by the
// user (training events) and which quietbox moved itself (the audit log
// explains them). It does no I/O.

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
	}
	for _, r := range rows {
		if (r.Action != store.ActionMove && r.Action != store.ActionUndoMove) || !auditDone(r) {
			continue
		}
		if r.DestUID != 0 {
			o.dests[destKey{r.ToFolder, r.DestUIDValidity, r.DestUID}] = true
		} else {
			o.keyFolder[keyFolder{r.MessageKey, r.ToFolder}] = true
		}
		if r.Action == store.ActionMove && !r.Undone {
			if at, ok := o.moved[r.MessageKey]; !ok || r.At.After(at) {
				o.moved[r.MessageKey] = r.At
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
// a return needs a stored date (from known, else from our own move) that the
// arrival's INTERNALDATE is not after.
func inboxArrival(a arrival, inbox string, known map[string]store.MessageRow, own *ownMoves, me map[string]bool) (returnVerdict, trainEvent) {
	isReturn := false
	if row, ok := known[a.Key]; ok && !a.H.InternalDate.After(row.InternalDate) {
		isReturn = true
	} else if at, ok := own.movedAt(a.Key); ok && !a.H.InternalDate.After(at) {
		isReturn = true
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
