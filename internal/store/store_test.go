package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	if _, err := s.db.Exec(`DROP TABLE meta; DROP TABLE sent_message_ids; DROP TABLE cursors;
		ALTER TABLE audit DROP COLUMN status;
		ALTER TABLE audit DROP COLUMN dest_uidvalidity;
		ALTER TABLE audit DROP COLUMN dest_uid;
		DROP TABLE messages; DROP TABLE training; PRAGMA user_version = 1`); err != nil {
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

func TestMigration3KeepsOldAuditRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a database left at schema v2 holding a create_folder row.
	if _, err := s.db.Exec(`DROP TABLE meta; DROP TABLE cursors;
		ALTER TABLE audit DROP COLUMN status;
		ALTER TABLE audit DROP COLUMN dest_uidvalidity;
		ALTER TABLE audit DROP COLUMN dest_uid;
		INSERT INTO audit(action, to_folder, reason, at) VALUES ('create_folder','X','r',1);
		DROP TABLE messages; DROP TABLE training; PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = s.Close() }()
	rows, err := s.Audit(ctx)
	if err != nil || len(rows) != 1 || rows[0].Status != AuditDone {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestAuditPlannedThenDone(t *testing.T) {
	s := openTest(t)
	id, err := s.AddAudit(ctx, AuditRow{Action: ActionMove, MessageKey: "m:<a>", FromFolder: "INBOX", ToFolder: "News", Reason: "r", At: at(1), Status: AuditPlanned})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := s.Audit(ctx)
	if rows[0].Status != AuditPlanned {
		t.Fatalf("status = %q", rows[0].Status)
	}
	if err := s.SetAuditResult(ctx, id, AuditDone, 7, 42); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Audit(ctx)
	if r := rows[0]; r.Status != AuditDone || r.DestUIDValidity != 7 || r.DestUID != 42 {
		t.Fatalf("row = %+v", r)
	}
}

func TestMovedKeysCountsEveryStatus(t *testing.T) {
	s := openTest(t)
	for i, st := range []string{AuditPlanned, AuditDone, AuditFailed} {
		if _, err := s.AddAudit(ctx, AuditRow{Action: ActionMove, MessageKey: "m:<" + st + ">", ToFolder: "F", Reason: "r", At: at(i + 1), Status: st}); err != nil {
			t.Fatal(err)
		}
	}
	id, _ := s.AddAudit(ctx, AuditRow{Action: ActionMove, MessageKey: "m:<undone>", ToFolder: "F", Reason: "r", At: at(5), Status: AuditDone})
	if err := s.MarkUndone(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, _ = s.AddAudit(ctx, AuditRow{Action: "create_folder", MessageKey: "m:<folder>", ToFolder: "F", Reason: "r", At: at(6)})
	_, _ = s.AddAudit(ctx, AuditRow{Action: ActionUndoMove, MessageKey: "m:<undomove>", ToFolder: "F", Reason: "r", At: at(7)})
	got, err := s.MovedKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("keys = %v", got)
	}
	for _, k := range []string{"m:<planned>", "m:<done>", "m:<failed>", "m:<undone>"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
}

func TestAuditSinceNewestFirstFiltersAction(t *testing.T) {
	s := openTest(t)
	_, _ = s.AddAudit(ctx, AuditRow{Action: ActionMove, MessageKey: "old", Reason: "r", At: at(1), Status: AuditDone})
	_, _ = s.AddAudit(ctx, AuditRow{Action: ActionMove, MessageKey: "a", Reason: "r", At: at(10), Status: AuditDone})
	_, _ = s.AddAudit(ctx, AuditRow{Action: "create_folder", Reason: "r", At: at(11)})
	_, _ = s.AddAudit(ctx, AuditRow{Action: ActionMove, MessageKey: "b", Reason: "r", At: at(12), Status: AuditDone})
	rows, err := s.AuditSince(ctx, at(5), ActionMove)
	if err != nil || len(rows) != 2 || rows[0].MessageKey != "b" || rows[1].MessageKey != "a" {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	s := openTest(t)
	if _, ok, err := s.Cursor(ctx, "INBOX"); err != nil || ok {
		t.Fatalf("missing cursor: ok=%v err=%v", ok, err)
	}
	if err := s.SetCursor(ctx, Cursor{Folder: "INBOX", UIDValidity: 3, NextUID: 10}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCursor(ctx, Cursor{Folder: "INBOX", UIDValidity: 3, NextUID: 15}); err != nil {
		t.Fatal(err)
	}
	c, ok, err := s.Cursor(ctx, "INBOX")
	if err != nil || !ok || c.NextUID != 15 || c.UIDValidity != 3 {
		t.Fatalf("cursor = %+v ok=%v err=%v", c, ok, err)
	}
}

func TestMigration4KeepsOldData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCursor(ctx, Cursor{Folder: "INBOX", UIDValidity: 2, NextUID: 9}); err != nil {
		t.Fatal(err)
	}
	// Simulate a database left at schema v3 (no meta table, no backlog column).
	if _, err := s.db.Exec(`DROP TABLE messages; DROP TABLE training; DROP TABLE meta; ALTER TABLE cursors DROP COLUMN backlog_below; PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = s.Close() }()
	c, ok, err := s.Cursor(ctx, "INBOX")
	if err != nil || !ok || c.NextUID != 9 || c.UIDValidity != 2 || c.BacklogBelow != 0 {
		t.Fatalf("cursor = %+v ok=%v err=%v", c, ok, err)
	}
}

func TestBindAccountAdoptsThenRefusesOther(t *testing.T) {
	s := openTest(t)
	if err := s.BindAccount(ctx, "me@imap.a.test"); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if err := s.BindAccount(ctx, "me@imap.a.test"); err != nil {
		t.Fatalf("same account: %v", err)
	}
	err := s.BindAccount(ctx, "other@imap.b.test")
	if !errors.Is(err, ErrWrongAccount) {
		t.Fatalf("err = %v, want ErrWrongAccount", err)
	}
	for _, want := range []string{"me@imap.a.test", "other@imap.b.test", "[state] path"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err, want)
		}
	}
}

func TestCursorBacklogRoundTrip(t *testing.T) {
	s := openTest(t)
	want := Cursor{Folder: "INBOX", UIDValidity: 4, NextUID: 10, BacklogBelow: 50}
	if err := s.SetCursor(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Cursor(ctx, "INBOX"); got != want {
		t.Fatalf("Store.SetCursor: %+v", got)
	}
	want.NextUID, want.BacklogBelow = 20, 0
	if err := s.Update(ctx, func(tx *Tx) error { return tx.SetCursor(want) }); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.Cursor(ctx, "INBOX"); got != want {
		t.Fatalf("Tx.SetCursor: %+v", got)
	}
}
