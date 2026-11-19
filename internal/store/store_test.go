package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var ctx = context.Background()

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func at(day int) time.Time { return time.Date(2026, 5, day, 12, 0, 0, 0, time.UTC) }

func TestOpenMigratesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d", "q.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.version()
	if err != nil || v != len(migrations) {
		t.Fatalf("version = %d, %v", v, err)
	}
	_ = s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = s2.Close()
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("expected error for a database from a newer quietbox")
	}
}

func TestOpenExistingDoesNotCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "q.db")
	if _, err := OpenExisting(path); !errors.Is(err, ErrNotInitialised) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Error("OpenExisting must not create anything")
	}
}

func TestReplaceObservationsScope(t *testing.T) {
	s := openTest(t)
	all := []Obs{
		{"a@x.test", "News", at(1)}, {"a@x.test", "Bulk", at(2)}, {"b@x.test", "News", at(3)},
	}
	err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceObservations(nil, all) })
	if err != nil {
		t.Fatal(err)
	}
	// Re-learn News only: Bulk rows must survive, News rows are replaced.
	err = s.Update(ctx, func(tx *Tx) error {
		return tx.ReplaceObservations([]string{"News"}, []Obs{{"c@x.test", "News", at(9)}})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Observations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	byAddr := map[string]Obs{}
	for _, o := range got {
		byAddr[o.Address] = o
	}
	if o := byAddr["a@x.test"]; o.Folder != "Bulk" || !o.At.Equal(at(2)) {
		t.Errorf("Bulk row lost or altered: %+v", o)
	}
	if o := byAddr["c@x.test"]; o.Folder != "News" {
		t.Errorf("new News row missing: %+v", o)
	}
	// nil scope clears everything.
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceObservations(nil, nil) }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Observations(ctx); len(got) != 0 {
		t.Errorf("not cleared: %v", got)
	}
}

func TestSenderFolderCountsView(t *testing.T) {
	s := openTest(t)
	obs := []Obs{{"a@x.test", "News", at(1)}, {"a@x.test", "News", at(5)}, {"a@x.test", "Bulk", at(2)}}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceObservations(nil, obs) }); err != nil {
		t.Fatal(err)
	}
	counts, err := s.FolderCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]FolderCount{}
	for _, c := range counts {
		m[c.Address+"|"+c.Folder] = c
	}
	if c := m["a@x.test|News"]; c.Count != 2 || !c.LastSeen.Equal(at(5)) {
		t.Errorf("News = %+v", c)
	}
	if c := m["a@x.test|Bulk"]; c.Count != 1 {
		t.Errorf("Bulk = %+v", c)
	}
}

func TestSentTo(t *testing.T) {
	s := openTest(t)
	err := s.Update(ctx, func(tx *Tx) error {
		return tx.ReplaceSentTo([]SentRow{{"a@x.test", 2, at(3)}, {"b@x.test", 1, at(4)}})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SentTo(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	// Replace is total.
	_ = s.Update(ctx, func(tx *Tx) error { return tx.ReplaceSentTo([]SentRow{{"c@x.test", 1, at(5)}}) })
	if got, _ := s.SentTo(ctx); len(got) != 1 || got[0].Address != "c@x.test" {
		t.Errorf("got %v", got)
	}
}

func TestSentMessageIDs(t *testing.T) {
	s := openTest(t)
	err := s.Update(ctx, func(tx *Tx) error {
		return tx.ReplaceSentMessageIDs([]SentMsg{{"<a@x.test>", at(3)}, {"<b@x.test>", at(4)}, {"<a@x.test>", at(3)}})
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SentMessageIDs(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, id := range []string{"<a@x.test>", "<b@x.test>"} {
		if _, ok := got[id]; !ok {
			t.Errorf("missing %s", id)
		}
	}
	// Replace is total.
	_ = s.Update(ctx, func(tx *Tx) error { return tx.ReplaceSentMessageIDs([]SentMsg{{"<c@x.test>", at(5)}}) })
	if got, _ := s.SentMessageIDs(ctx); len(got) != 1 {
		t.Errorf("got %v", got)
	}
	// Empty message IDs are never stored.
	_ = s.Update(ctx, func(tx *Tx) error { return tx.ReplaceSentMessageIDs([]SentMsg{{"", at(5)}}) })
	if got, _ := s.SentMessageIDs(ctx); len(got) != 0 {
		t.Errorf("empty id stored: %v", got)
	}
}

func TestMigrationV2KeepsV1Data(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Update(ctx, func(tx *Tx) error { return tx.ReplaceSentTo([]SentRow{{"a@x.test", 2, at(3)}}) })
	// Simulate a database left at schema v1.
	if _, err := s.db.Exec("DROP TABLE sent_message_ids; PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = s.Close() }()
	if got, _ := s.SentTo(ctx); len(got) != 1 {
		t.Errorf("v1 data lost: %v", got)
	}
	if got, err := s.SentMessageIDs(ctx); err != nil || len(got) != 0 {
		t.Errorf("new table: %v, %v", got, err)
	}
}

func TestReplaceSendersKeepsTrained(t *testing.T) {
	s := openTest(t)
	first := []SenderRow{
		{Address: "t@x.test", Domain: "x.test", RouteFolder: "Bulk", RouteSource: SourceTrained, Reason: "moved"},
		{Address: "old@x.test", Domain: "x.test", RouteFolder: "News", RouteSource: SourceLearned, Reason: "r"},
	}
	// Seed directly: a trained row can only exist from M4, but the guarantee starts now.
	if err := s.Update(ctx, func(tx *Tx) error { return tx.insertSenders(first) }); err != nil {
		t.Fatal(err)
	}
	relearn := []SenderRow{
		{Address: "t@x.test", Domain: "x.test", RouteFolder: "Later", RouteSource: SourceLearned, Reason: "r"},
		{Address: "new@x.test", Domain: "x.test", RouteFolder: "News", RouteSource: SourceLearned, Mixed: true, SentTo: 2, Reason: "r", UpdatedAt: at(7)},
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceSenders(relearn) }); err != nil {
		t.Fatal(err)
	}
	got, err := s.Senders(ctx, SenderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]SenderRow{}
	for _, r := range got {
		by[r.Address] = r
	}
	if len(by) != 2 {
		t.Fatalf("got %v", by)
	}
	if r := by["t@x.test"]; r.RouteFolder != "Bulk" || r.RouteSource != SourceTrained {
		t.Errorf("trained row was overwritten: %+v", r)
	}
	if _, ok := by["old@x.test"]; ok {
		t.Error("stale learned row should be gone")
	}
	if r := by["new@x.test"]; !r.Mixed || r.SentTo != 2 || !r.UpdatedAt.Equal(at(7)) {
		t.Errorf("new row = %+v", r)
	}
}

func TestSendersFilter(t *testing.T) {
	s := openTest(t)
	rows := []SenderRow{
		{Address: "a@x.test", Domain: "x.test", RouteFolder: "News", RouteSource: SourceLearned, Reason: "r"},
		{Address: "b@x.test", Domain: "x.test", RouteFolder: "News", RouteSource: SourceLearned, Mixed: true, Reason: "r"},
		{Address: "c@x.test", Domain: "x.test", RouteFolder: "Bulk", RouteSource: SourceLearned, Mixed: true, Reason: "r"},
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceSenders(rows) }); err != nil {
		t.Fatal(err)
	}
	check := func(f SenderFilter, want ...string) {
		t.Helper()
		got, err := s.Senders(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("%+v: got %v, want %v", f, got, want)
		}
		for i := range want {
			if got[i].Address != want[i] {
				t.Errorf("%+v: got[%d] = %s, want %s", f, i, got[i].Address, want[i])
			}
		}
	}
	check(SenderFilter{}, "a@x.test", "b@x.test", "c@x.test")
	check(SenderFilter{Folder: "News"}, "a@x.test", "b@x.test")
	check(SenderFilter{Mixed: true}, "b@x.test", "c@x.test")
	check(SenderFilter{Folder: "News", Mixed: true}, "b@x.test")
}

func TestDomainsAndFolders(t *testing.T) {
	s := openTest(t)
	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.ReplaceDomains([]DomainRow{{"substack.test", "News", 5, 4}}); err != nil {
			return err
		}
		return tx.UpsertFolder(FolderRow{Name: "News", Type: "newsletter", Present: true, LastScanned: at(2)})
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Domains(ctx)
	if err != nil || len(d) != 1 || d[0].Agreeing != 4 {
		t.Fatalf("domains = %v, %v", d, err)
	}
	f, err := s.Folders(ctx)
	if err != nil || len(f) != 1 || !f[0].Present || !f[0].LastScanned.Equal(at(2)) {
		t.Fatalf("folders = %v, %v", f, err)
	}
}

func TestUpdateRollsBack(t *testing.T) {
	s := openTest(t)
	boom := errors.New("boom")
	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.ReplaceObservations(nil, []Obs{{"a@x.test", "News", at(1)}}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := s.Observations(ctx); len(got) != 0 {
		t.Errorf("rollback failed: %v", got)
	}
}

func TestAuditSurvivesAndIsOrdered(t *testing.T) {
	s := openTest(t)
	id1, err := s.AddAudit(ctx, AuditRow{Action: "create_folder", ToFolder: "SaneReceipts", Reason: "configured folder missing", At: at(1)})
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.AddAudit(ctx, AuditRow{Action: "create_folder", ToFolder: "Other", Reason: "r", At: at(2)})
	if id2 <= id1 {
		t.Errorf("ids not increasing: %d %d", id1, id2)
	}
	rows, err := s.Audit(ctx)
	if err != nil || len(rows) != 2 || rows[0].ToFolder != "SaneReceipts" || rows[0].Undone {
		t.Fatalf("audit = %+v, %v", rows, err)
	}
}
