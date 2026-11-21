package engine

import (
	"testing"
	"time"

	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

var (
	trainT0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)  // delivery (INTERNALDATE)
	trainT1 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)  // our move
	trainT2 = time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC) // a later fresh delivery
)

func trainArrival(folder string, validity, uid uint32, from string, internal time.Time) arrival {
	h := mail.Header{Folder: folder, UID: uid, From: from, InternalDate: internal, MessageID: "<m1@x>"}
	return arrival{H: h, Key: mail.Key(h), Folder: folder, Validity: validity}
}

func TestOwnMovesExplains(t *testing.T) {
	key := mail.Key(mail.Header{MessageID: "<m1@x>"})
	ourMove := store.AuditRow{Action: store.ActionMove, MessageKey: key, FromFolder: "INBOX", ToFolder: "SaneNews",
		At: trainT1, Status: store.AuditDone, DestUIDValidity: 100, DestUID: 7}
	undoMove := store.AuditRow{Action: store.ActionUndoMove, MessageKey: key, FromFolder: "SaneNews", ToFolder: "INBOX",
		At: trainT1, Status: store.AuditDone, DestUIDValidity: 1, DestUID: 42}
	with := func(r store.AuditRow, f func(*store.AuditRow)) store.AuditRow { f(&r); return r }

	tests := []struct {
		name string
		rows []store.AuditRow
		a    arrival
		want bool
	}{
		{"our move to SaneNews uid 7", []store.AuditRow{ourMove},
			trainArrival("SaneNews", 100, 7, "a@x.com", trainT0), true},
		{"dragged back and into SaneNews again with uid 9", []store.AuditRow{ourMove},
			trainArrival("SaneNews", 100, 9, "a@x.com", trainT0), false},
		{"same uid, other uidvalidity", []store.AuditRow{ourMove},
			trainArrival("SaneNews", 101, 7, "a@x.com", trainT0), false},
		{"same uid, other folder", []store.AuditRow{ourMove},
			trainArrival("SaneLater", 100, 7, "a@x.com", trainT0), false},
		{"undo_move row to INBOX", []store.AuditRow{undoMove},
			trainArrival("INBOX", 1, 42, "a@x.com", trainT0), true},
		{"empty status is stored as done", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.Status = "" })},
			trainArrival("SaneNews", 100, 7, "a@x.com", trainT0), true},
		// A move whose outcome was not recorded `done` may still have happened
		// (crash, or a dropped connection after the server moved it): its key
		// in its destination is ours. A user move of the same message into the
		// same folder is missed, which is far cheaper than a false route.
		{"planned row explains its key in its destination", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.Status = store.AuditPlanned })},
			trainArrival("SaneNews", 100, 7, "a@x.com", trainT0), true},
		{"failed row explains its key in its destination", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.Status = store.AuditFailed })},
			trainArrival("SaneNews", 100, 9, "a@x.com", trainT0), true},
		{"planned row with no dest explains its key in its destination", []store.AuditRow{with(ourMove, func(r *store.AuditRow) {
			r.Status, r.DestUIDValidity, r.DestUID = store.AuditPlanned, 0, 0
		})}, trainArrival("SaneNews", 100, 9, "a@x.com", trainT0), true},
		{"failed row, key matches but other folder", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.Status = store.AuditFailed })},
			trainArrival("SaneLater", 100, 9, "a@x.com", trainT0), false},
		{"failed row, folder matches but other key", []store.AuditRow{with(ourMove, func(r *store.AuditRow) {
			r.Status, r.MessageKey = store.AuditFailed, "m:<other@x>"
		})}, trainArrival("SaneNews", 100, 9, "a@x.com", trainT0), false},
		{"planned undo_move explains its key back in INBOX", []store.AuditRow{with(undoMove, func(r *store.AuditRow) {
			r.Status, r.DestUIDValidity, r.DestUID = store.AuditPlanned, 0, 0
		})}, trainArrival("INBOX", 1, 50, "a@x.com", trainT0), true},
		{"failed undo_move explains its key back in INBOX", []store.AuditRow{with(undoMove, func(r *store.AuditRow) {
			r.Status, r.DestUIDValidity, r.DestUID = store.AuditFailed, 0, 0
		})}, trainArrival("INBOX", 1, 50, "a@x.com", trainT0), true},
		{"create_folder row explains nothing", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.Action = store.ActionCreateFolder })},
			trainArrival("SaneNews", 100, 7, "a@x.com", trainT0), false},
		{"DestUID 0 falls back to key+folder", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.DestUIDValidity, r.DestUID = 0, 0 })},
			trainArrival("SaneNews", 100, 9, "a@x.com", trainT0), true},
		{"DestUID 0, key matches but other folder", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.DestUIDValidity, r.DestUID = 0, 0 })},
			trainArrival("SaneLater", 100, 9, "a@x.com", trainT0), false},
		{"DestUID 0, folder matches but other key", []store.AuditRow{with(ourMove, func(r *store.AuditRow) {
			r.DestUIDValidity, r.DestUID, r.MessageKey = 0, 0, "m:<other@x>"
		})}, trainArrival("SaneNews", 100, 9, "a@x.com", trainT0), false},
		{"undone DestUID 0 row does not explain a later arrival of the key", []store.AuditRow{with(ourMove, func(r *store.AuditRow) {
			r.DestUIDValidity, r.DestUID, r.Undone = 0, 0, true
		})}, trainArrival("SaneNews", 100, 9, "a@x.com", trainT0), false},
		{"undone row with a dest UID still explains its own arrival", []store.AuditRow{with(ourMove, func(r *store.AuditRow) { r.Undone = true })},
			trainArrival("SaneNews", 100, 7, "a@x.com", trainT0), true},
		{"no rows", nil, trainArrival("SaneNews", 100, 7, "a@x.com", trainT0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := newOwnMoves(tt.rows).explains(tt.a); got != tt.want {
				t.Errorf("explains = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOwnMovesMovedAt(t *testing.T) {
	key := "m:<m1@x>"
	mv := func(at time.Time, status string, undone bool) store.AuditRow {
		return store.AuditRow{Action: store.ActionMove, MessageKey: key, ToFolder: "SaneNews", At: at, Status: status, Undone: undone}
	}
	tests := []struct {
		name   string
		rows   []store.AuditRow
		want   time.Time
		wantOK bool
	}{
		{"none", nil, time.Time{}, false},
		{"one done move", []store.AuditRow{mv(trainT1, store.AuditDone, false)}, trainT1, true},
		{"latest of several", []store.AuditRow{mv(trainT2, store.AuditDone, false), mv(trainT1, store.AuditDone, false)}, trainT2, true},
		{"undone ignored", []store.AuditRow{mv(trainT1, store.AuditDone, false), mv(trainT2, store.AuditDone, true)}, trainT1, true},
		{"only undone", []store.AuditRow{mv(trainT1, store.AuditDone, true)}, time.Time{}, false},
		{"planned and failed ignored", []store.AuditRow{mv(trainT1, store.AuditPlanned, false), mv(trainT2, store.AuditFailed, false)}, time.Time{}, false},
		{"undo_move ignored", []store.AuditRow{{Action: store.ActionUndoMove, MessageKey: key, At: trainT1, Status: store.AuditDone}}, time.Time{}, false},
		{"other key ignored", []store.AuditRow{{Action: store.ActionMove, MessageKey: "m:<other@x>", At: trainT1, Status: store.AuditDone}}, time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := newOwnMoves(tt.rows).movedAt(key)
			if ok != tt.wantOK || !got.Equal(tt.want) {
				t.Errorf("movedAt = %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestFileEvent(t *testing.T) {
	me := map[string]bool{"me@quietbox.test": true}
	tests := []struct {
		name   string
		from   string
		want   trainEvent
		wantOK bool
	}{
		{"normal", "news@shop.com", trainEvent{Sender: "news@shop.com", Folder: "SaneNews", MessageKey: "m:<m1@x>"}, true},
		{"From in me", "me@quietbox.test", trainEvent{}, false},
		{"empty From", "", trainEvent{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := trainArrival("SaneNews", 100, 7, tt.from, trainT0)
			got, ok := fileEvent(a, me)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("fileEvent = %+v, %v; want %+v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestInboxArrival(t *testing.T) {
	const inbox = "INBOX"
	me := map[string]bool{"me@quietbox.test": true}
	key := mail.Key(mail.Header{MessageID: "<m1@x>"})
	knownRow := map[string]store.MessageRow{key: {Key: key, Folder: "SaneNews", InternalDate: trainT0}}
	moveRow := store.AuditRow{Action: store.ActionMove, MessageKey: key, FromFolder: inbox, ToFolder: "SaneNews",
		At: trainT1, Status: store.AuditDone, DestUIDValidity: 100, DestUID: 7}
	undoRow := store.AuditRow{Action: store.ActionUndoMove, MessageKey: key, FromFolder: "SaneNews", ToFolder: inbox,
		At: trainT2, Status: store.AuditDone, DestUIDValidity: 1, DestUID: 42}
	undoneMove := moveRow
	undoneMove.Undone = true
	trained := trainEvent{Sender: "news@shop.com", Folder: inbox, MessageKey: key}

	tests := []struct {
		name      string
		a         arrival
		known     map[string]store.MessageRow
		rows      []store.AuditRow
		want      returnVerdict
		wantEvent trainEvent
	}{
		{"known key, same internal date", trainArrival(inbox, 1, 50, "news@shop.com", trainT0),
			knownRow, nil, returnTrained, trained},
		{"known key, earlier internal date", trainArrival(inbox, 1, 50, "news@shop.com", trainT0.Add(-time.Hour)),
			knownRow, nil, returnTrained, trained},
		// A known key that is not a return (a later copy, or a server that
		// restamps INTERNALDATE on a move) is left in INBOX: never classified,
		// never trained.
		{"known key, later internal date (fresh duplicate or restamped): left alone", trainArrival(inbox, 1, 50, "news@shop.com", trainT2),
			knownRow, nil, knownNotReturn, trainEvent{}},
		{"known key, zero internal dates are never a return: left alone", trainArrival(inbox, 1, 50, "news@shop.com", time.Time{}),
			map[string]store.MessageRow{key: {Key: key, Folder: "SaneNews"}}, nil, knownNotReturn, trainEvent{}},
		{"known key, zero arrival date is never a return: left alone", trainArrival(inbox, 1, 50, "news@shop.com", time.Time{}),
			knownRow, nil, knownNotReturn, trainEvent{}},
		{"audit row only, zero arrival date is never a return: left alone", trainArrival(inbox, 1, 50, "news@shop.com", time.Time{}),
			nil, []store.AuditRow{moveRow}, knownNotReturn, trainEvent{}},
		{"known row says not a return, audit fallback not consulted: left alone",
			trainArrival(inbox, 1, 50, "news@shop.com", trainT0.Add(12*time.Hour)), // T0 < date < T1
			knownRow, []store.AuditRow{moveRow}, knownNotReturn, trainEvent{}},
		{"known via audit row only, internal date before move time", trainArrival(inbox, 1, 50, "news@shop.com", trainT0),
			nil, []store.AuditRow{moveRow}, returnTrained, trained},
		{"known via audit row only, internal date equal to move time", trainArrival(inbox, 1, 50, "news@shop.com", trainT1),
			nil, []store.AuditRow{moveRow}, returnTrained, trained},
		{"known via audit row only, internal date after move time: left alone", trainArrival(inbox, 1, 50, "news@shop.com", trainT2),
			nil, []store.AuditRow{moveRow}, knownNotReturn, trainEvent{}},
		{"undone move row", trainArrival(inbox, 1, 50, "news@shop.com", trainT0),
			nil, []store.AuditRow{undoneMove}, notReturn, trainEvent{}},
		{"explained by undo_move", trainArrival(inbox, 1, 42, "news@shop.com", trainT0),
			knownRow, []store.AuditRow{undoneMove, undoRow}, returnExplained, trainEvent{}},
		{"own address", trainArrival(inbox, 1, 50, "me@quietbox.test", trainT0),
			knownRow, nil, returnExplained, trainEvent{}},
		{"empty From", trainArrival(inbox, 1, 50, "", trainT0),
			knownRow, nil, returnExplained, trainEvent{}},
		{"unknown key", trainArrival(inbox, 1, 50, "news@shop.com", trainT0),
			map[string]store.MessageRow{"m:<other@x>": {Key: "m:<other@x>", InternalDate: trainT0}}, nil, notReturn, trainEvent{}},
		{"unknown key, nothing loaded", trainArrival(inbox, 1, 50, "news@shop.com", trainT0),
			nil, nil, notReturn, trainEvent{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ev := inboxArrival(tt.a, inbox, tt.known, newOwnMoves(tt.rows), me)
			if got != tt.want || ev != tt.wantEvent {
				t.Errorf("inboxArrival = %v, %+v; want %v, %+v", got, ev, tt.want, tt.wantEvent)
			}
		})
	}
}
