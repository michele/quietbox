package engine

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/store"
)

// trainedBox is learnedMailbox where stranger@x.test (message <n5>) has a
// trained route to SaneLater, one training event and one audit row.
func trainedBox(t *testing.T) (uid uint32, o ExplainOptions, fake interface{ MoveCount() int }, st *store.Store, run func(ExplainOptions) (*Explanation, error)) {
	t.Helper()
	f, st, cfg := learnedMailbox(t)
	when := learnNow.Add(-time.Hour)
	err := st.Update(bg, func(tx *store.Tx) error {
		if err := tx.UpsertTrainedSender(store.SenderRow{Address: "stranger@x.test", Domain: "x.test", RouteFolder: "SaneLater", Reason: "you moved it", UpdatedAt: when}); err != nil {
			return err
		}
		return tx.AddTraining(store.TrainingRow{At: when, Sender: "stranger@x.test", Folder: "SaneLater", Source: store.TrainMove, MessageKey: "m:<old>"})
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range f.Messages["INBOX"] {
		if h.MessageID == "<n5>" {
			uid = h.UID
		}
	}
	if _, _, err := f.FetchOne(bg, "INBOX", 1); err != nil { // forces UIDs
		t.Fatal(err)
	}
	for _, h := range f.Messages["INBOX"] {
		if h.MessageID == "<n5>" {
			uid = h.UID
		}
	}
	if _, err := st.AddAudit(bg, store.AuditRow{Action: store.ActionMove, MessageKey: "m:<n5>", FromFolder: "INBOX", ToFolder: "SaneLater", Reason: "r", At: when}); err != nil {
		t.Fatal(err)
	}
	return uid, ExplainOptions{Folder: "INBOX"}, f, st, func(o ExplainOptions) (*Explanation, error) {
		return Explain(bg, cfg, f, st, o)
	}
}

func TestExplainUsesCurrentKnowledgeAndShowsTraining(t *testing.T) {
	uid, o, _, _, run := trainedBox(t)
	o.UID = uid
	e, err := run(o)
	if err != nil {
		t.Fatal(err)
	}
	if e.Decision.Folder != "SaneLater" {
		t.Errorf("decision = %+v, want SaneLater", e.Decision)
	}
	if e.Route == nil || e.Route.RouteFolder != "SaneLater" || e.Route.RouteSource != store.SourceTrained {
		t.Errorf("route = %+v", e.Route)
	}
	if len(e.Training) != 1 || e.Training[0].Folder != "SaneLater" {
		t.Errorf("training = %+v", e.Training)
	}
	if len(e.Audit) != 1 || e.Audit[0].ToFolder != "SaneLater" {
		t.Errorf("audit = %+v", e.Audit)
	}
	if e.Header.From != "stranger@x.test" || e.Header.UID != uid {
		t.Errorf("header = %+v", e.Header)
	}
}

func TestExplainByMessageID(t *testing.T) {
	uid, o, _, _, run := trainedBox(t)
	o.MessageID = "<n5>"
	e, err := run(o)
	if err != nil {
		t.Fatal(err)
	}
	if e.Header.UID != uid {
		t.Errorf("uid = %d, want %d", e.Header.UID, uid)
	}
}

func TestExplainUnknownMessageIsAnError(t *testing.T) {
	_, o, _, _, run := trainedBox(t)
	o.MessageID = "<nope>"
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("message-id err = %v", err)
	}
	o.MessageID, o.UID = "", 9999
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("uid err = %v", err)
	}
}

func TestExplainDoesNotMutate(t *testing.T) {
	uid, o, fake, st, run := trainedBox(t)
	o.UID = uid
	moves := fake.MoveCount()
	auditBefore, _ := st.Audit(bg)
	sendersBefore, _ := st.Senders(bg, store.SenderFilter{})
	trainBefore, _ := st.TrainingFor(bg, "stranger@x.test")
	if _, err := run(o); err != nil {
		t.Fatal(err)
	}
	auditAfter, _ := st.Audit(bg)
	sendersAfter, _ := st.Senders(bg, store.SenderFilter{})
	trainAfter, _ := st.TrainingFor(bg, "stranger@x.test")
	if fake.MoveCount() != moves || !reflect.DeepEqual(auditBefore, auditAfter) ||
		!reflect.DeepEqual(sendersBefore, sendersAfter) || !reflect.DeepEqual(trainBefore, trainAfter) {
		t.Error("explain changed state")
	}
}
