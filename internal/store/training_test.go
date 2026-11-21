package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigrateV4ToV5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:4] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 4"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = s.Close() }()
	if v, err := s.version(); err != nil || v != 5 {
		t.Fatalf("version = %d, %v", v, err)
	}
	for _, tb := range []string{"messages", "training"} {
		if !tableExists(t, s, tb) {
			t.Errorf("table %s missing", tb)
		}
	}
}

func TestUpsertTrainedSenderReplacesLearnedRowAndKeepsSentTo(t *testing.T) {
	s := openTest(t)
	learned := SenderRow{Address: "a@x.com", Domain: "x.com", RouteFolder: "Old", RouteSource: SourceLearned, Mixed: true, SentTo: 3, Reason: "r", UpdatedAt: at(1)}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceSenders([]SenderRow{learned}) }); err != nil {
		t.Fatal(err)
	}
	trained := SenderRow{Address: "a@x.com", Domain: "x.com", RouteFolder: "News", Reason: "moved", UpdatedAt: at(2), SentTo: 99}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.UpsertTrainedSender(trained) }); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Sender(ctx, "a@x.com")
	if err != nil || !ok {
		t.Fatalf("sender: %v ok=%v", err, ok)
	}
	if got.RouteFolder != "News" || got.RouteSource != SourceTrained || got.Mixed || got.SentTo != 3 || got.Reason != "moved" || !got.UpdatedAt.Equal(at(2)) {
		t.Fatalf("row = %+v", got)
	}
	// A new sender starts with its own SentTo.
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.UpsertTrainedSender(SenderRow{Address: "b@x.com", Domain: "x.com", RouteFolder: "News", SentTo: 2, UpdatedAt: at(2)})
	}); err != nil {
		t.Fatal(err)
	}
	if b, ok, _ := s.Sender(ctx, "b@x.com"); !ok || b.SentTo != 2 || b.RouteSource != SourceTrained {
		t.Fatalf("b = %+v ok=%v", b, ok)
	}
	if _, ok, err := s.Sender(ctx, "nobody@x.com"); err != nil || ok {
		t.Fatalf("missing sender: ok=%v err=%v", ok, err)
	}
}

func TestReplaceSendersKeepsTrainedRow(t *testing.T) {
	s := openTest(t)
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.UpsertTrainedSender(SenderRow{Address: "a@x.com", Domain: "x.com", RouteFolder: "News", UpdatedAt: at(1)})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.ReplaceSenders([]SenderRow{{Address: "a@x.com", Domain: "x.com", RouteFolder: "Other", RouteSource: SourceLearned, UpdatedAt: at(2)}})
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := s.Sender(ctx, "a@x.com")
	if !ok || got.RouteFolder != "News" || got.RouteSource != SourceTrained {
		t.Fatalf("row = %+v ok=%v", got, ok)
	}
}

func TestDeleteTrainedSenderIgnoresLearnedRows(t *testing.T) {
	s := openTest(t)
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.ReplaceSenders([]SenderRow{{Address: "l@x.com", Domain: "x.com", RouteFolder: "A", RouteSource: SourceLearned, UpdatedAt: at(1)}}); err != nil {
			return err
		}
		return tx.UpsertTrainedSender(SenderRow{Address: "t@x.com", Domain: "x.com", RouteFolder: "B", UpdatedAt: at(1)})
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeleteTrainedSender(ctx, "l@x.com"); err != nil || ok {
		t.Fatalf("learned: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := s.Sender(ctx, "l@x.com"); !ok {
		t.Fatal("learned row was deleted")
	}
	if ok, err := s.DeleteTrainedSender(ctx, "t@x.com"); err != nil || !ok {
		t.Fatalf("trained: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := s.Sender(ctx, "t@x.com"); ok {
		t.Fatal("trained row still there")
	}
}

func TestReplaceMessagesOnlyScope(t *testing.T) {
	s := openTest(t)
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.PutMessages([]MessageRow{
			{Key: "1", Folder: "A", InternalDate: at(1)},
			{Key: "2", Folder: "B", InternalDate: at(2)},
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error {
		return tx.ReplaceMessages([]string{"A"}, []MessageRow{{Key: "3", Folder: "A", InternalDate: at(3)}})
	}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Messages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["2"].Folder != "B" || m["3"].Folder != "A" || !m["3"].InternalDate.Equal(at(3)) {
		t.Fatalf("messages = %+v", m)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceMessages(nil, nil) }); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Messages(ctx); len(m) != 0 {
		t.Fatalf("nil scope should clear all: %+v", m)
	}
}

func TestMessagePutUpsertAndForget(t *testing.T) {
	s := openTest(t)
	if err := s.PutMessage(ctx, MessageRow{Key: "k", Folder: "A", InternalDate: at(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMessage(ctx, MessageRow{Key: "k", Folder: "B", InternalDate: at(1)}); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Messages(ctx); len(m) != 1 || m["k"].Folder != "B" {
		t.Fatalf("messages = %+v", m)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ForgetMessages([]string{"k", "zz"}) }); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Messages(ctx); len(m) != 0 {
		t.Fatalf("messages = %+v", m)
	}
	if err := s.ForgetMessage(ctx, "nope"); err != nil {
		t.Fatal(err)
	}
}

func TestTrainingForNewestFirst(t *testing.T) {
	s := openTest(t)
	if err := s.Update(ctx, func(tx *Tx) error {
		for i, src := range []string{TrainMove, TrainManual, TrainMove} {
			if err := tx.AddTraining(TrainingRow{At: at(i + 1), Sender: "a@x.com", Folder: fmt.Sprint("F", i), Source: src, MessageKey: fmt.Sprint("k", i)}); err != nil {
				return err
			}
		}
		return tx.AddTraining(TrainingRow{At: at(9), Sender: "other@x.com", Folder: "Z", Source: TrainMove})
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.TrainingFor(ctx, "a@x.com")
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %+v err=%v", rows, err)
	}
	if rows[0].Folder != "F2" || rows[1].Folder != "F1" || rows[2].Folder != "F0" {
		t.Fatalf("order = %+v", rows)
	}
	if rows[1].Source != TrainManual || rows[0].MessageKey != "k2" || !rows[0].At.Equal(at(3)) || rows[0].ID == 0 {
		t.Fatalf("fields = %+v", rows)
	}
}

func TestLatestTraining(t *testing.T) {
	s := openTest(t)
	if err := s.Update(ctx, func(tx *Tx) error {
		if _, ok, err := tx.LatestTraining("a@x.com"); err != nil || ok {
			return fmt.Errorf("empty table: ok=%v err=%v", ok, err)
		}
		for i := range 3 {
			if err := tx.AddTraining(TrainingRow{At: at(i + 1), Sender: "a@x.com", Folder: fmt.Sprint("F", i), Source: TrainMove, MessageKey: fmt.Sprint("k", i)}); err != nil {
				return err
			}
		}
		r, ok, err := tx.LatestTraining("a@x.com")
		if err != nil || !ok || r.Folder != "F2" || r.MessageKey != "k2" || !r.At.Equal(at(3)) {
			return fmt.Errorf("latest = %+v ok=%v err=%v", r, ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteCursor(t *testing.T) {
	s := openTest(t)
	if err := s.SetCursor(ctx, Cursor{Folder: "SaneNews", UIDValidity: 1, NextUID: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCursor(ctx, "SaneNews"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Cursor(ctx, "SaneNews"); err != nil || ok {
		t.Errorf("cursor still there: ok=%v err=%v", ok, err)
	}
}
