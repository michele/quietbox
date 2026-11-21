package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/store"
)

func setTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSetSenderRouteValidatesFolder(t *testing.T) {
	ctx := context.Background()
	cfg := learnConfig(t, "[learn]\nsent_folder = \"SaneBulk\"\n")
	st := setTestStore(t)
	for _, bad := range []string{"Nope", "Sent", "SaneBulk", "inbox"} {
		err := SetSenderRoute(ctx, cfg, st, "a@x.com", bad, learnNow)
		if err == nil || !strings.Contains(err.Error(), "SaneLater") || !strings.Contains(err.Error(), "INBOX") {
			t.Errorf("folder %q: err = %v, want one naming the valid folders", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), "SaneBulk,") {
			t.Errorf("folder %q: error lists the Sent folder as valid: %v", bad, err)
		}
	}
	if _, ok, _ := st.Sender(ctx, "a@x.com"); ok {
		t.Fatal("a rejected folder wrote a row")
	}
	if err := SetSenderRoute(ctx, cfg, st, "A@X.com", "INBOX", learnNow); err != nil {
		t.Fatalf("INBOX: %v", err)
	}
	r, ok, _ := st.Sender(ctx, "a@x.com")
	if !ok || r.RouteFolder != "INBOX" {
		t.Fatalf("row = %+v ok=%v", r, ok)
	}
}

func TestSetSenderRouteOverwritesLearnedRow(t *testing.T) {
	ctx := context.Background()
	cfg := learnConfig(t, "")
	st := setTestStore(t)
	if err := st.Update(ctx, func(tx *store.Tx) error {
		return tx.ReplaceSenders([]store.SenderRow{{Address: "a@x.com", Domain: "x.com", RouteFolder: "SaneBulk",
			RouteSource: store.SourceLearned, Mixed: true, SentTo: 4, Reason: "x", UpdatedAt: learnNow}})
	}); err != nil {
		t.Fatal(err)
	}
	at := learnNow.Add(time.Hour)
	if err := SetSenderRoute(ctx, cfg, st, "A@X.com", "SaneNews", at); err != nil {
		t.Fatal(err)
	}
	r, ok, _ := st.Sender(ctx, "a@x.com")
	if !ok || r.RouteFolder != "SaneNews" || r.RouteSource != store.SourceTrained || r.Mixed ||
		r.Reason != "set by hand" || r.SentTo != 4 || r.Domain != "x.com" {
		t.Fatalf("row = %+v", r)
	}
	tr, _ := st.TrainingFor(ctx, "a@x.com")
	if len(tr) != 1 || tr[0].Source != store.TrainManual || tr[0].Folder != "SaneNews" || !tr[0].At.Equal(at) {
		t.Fatalf("training = %+v", tr)
	}
}

func TestUnsetSenderRouteOnlyTrained(t *testing.T) {
	ctx := context.Background()
	cfg := learnConfig(t, "")
	st := setTestStore(t)
	if ok, err := UnsetSenderRoute(ctx, st, "a@x.com"); err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}
	if err := SetSenderRoute(ctx, cfg, st, "a@x.com", "SaneNews", learnNow); err != nil {
		t.Fatal(err)
	}
	if ok, err := UnsetSenderRoute(ctx, st, "A@X.com"); err != nil || !ok {
		t.Fatalf("trained: ok=%v err=%v", ok, err)
	}
	// A learned row is left alone.
	if err := st.Update(ctx, func(tx *store.Tx) error {
		return tx.ReplaceSenders([]store.SenderRow{{Address: "b@x.com", Domain: "x.com", RouteFolder: "SaneBulk",
			RouteSource: store.SourceLearned, UpdatedAt: learnNow}})
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := UnsetSenderRoute(ctx, st, "b@x.com"); err != nil || ok {
		t.Fatalf("learned: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := st.Sender(ctx, "b@x.com"); !ok {
		t.Fatal("learned row was removed")
	}
}
