package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateV5ToV6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:5] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 5"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer func() { _ = s.Close() }()
	if v, err := s.version(); err != nil || v != 6 {
		t.Fatalf("version = %d, %v", v, err)
	}
	if !tableExists(t, s, "blackhole_seen") {
		t.Error("table blackhole_seen missing")
	}
}

func TestBlackholeSeenRoundTripAndReplace(t *testing.T) {
	s := openTest(t)
	got, err := s.BlackholeSeen(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
	first := map[string]SeenRow{"m:a": {Key: "m:a", FirstSeen: at(1)}, "m:b": {Key: "m:b", FirstSeen: at(2)}}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceBlackholeSeen(first) }); err != nil {
		t.Fatal(err)
	}
	got, err = s.BlackholeSeen(ctx)
	if err != nil || len(got) != 2 || !got["m:a"].FirstSeen.Equal(at(1)) || !got["m:b"].FirstSeen.Equal(at(2)) {
		t.Fatalf("got %v, %v", got, err)
	}
	// Replacing drops the keys that are no longer given.
	second := map[string]SeenRow{"m:b": {Key: "m:b", FirstSeen: at(2)}, "m:c": {Key: "m:c", FirstSeen: at(3)}}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.ReplaceBlackholeSeen(second) }); err != nil {
		t.Fatal(err)
	}
	got, _ = s.BlackholeSeen(ctx)
	if _, ok := got["m:a"]; ok || len(got) != 2 {
		t.Fatalf("m:a should be gone: %v", got)
	}
}

func TestMetaValue(t *testing.T) {
	s := openTest(t)
	if v, ok, err := s.Meta(ctx, "blackhole_swept_at"); err != nil || ok || v != "" {
		t.Fatalf("unset = %q, %v, %v", v, ok, err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.SetMeta("blackhole_swept_at", "100") }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, func(tx *Tx) error { return tx.SetMeta("blackhole_swept_at", "200") }); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.Meta(ctx, "blackhole_swept_at"); err != nil || !ok || v != "200" {
		t.Fatalf("got %q, %v, %v", v, ok, err)
	}
}
