//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/michele/quietbox/internal/store"
)

// M5: the blackhole retention sweep against the Dovecot test server. A real 30
// days cannot pass inside a test, so the stored first-seen times and the time
// of the last sweep are moved back through the database, which is exactly what
// the passing of time does to them.

// newBlackholeEnv is a throwaway user with a blackhole folder, a config with
// the default 30d retention, and `init` already run.
func newBlackholeEnv(t *testing.T) *m3Env {
	t.Helper()
	e := newBareEnv(t)
	c, done := rawLogin(t, e.user)
	if err := c.Create("SaneBlackHole", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	done()
	cfg := m3Config(e.user, e.db) + "\n[[folder]]\nname = \"SaneBlackHole\"\ntype = \"blackhole\"\n\n[blackhole]\nretention = \"30d\"\n"
	if err := os.WriteFile(e.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	e.deliver("INBOX", "alice@friends.test", "<m5-alice@t>")
	if out, err := e.run("init", "--yes", "--report", filepath.Join(filepath.Dir(e.cfg), "report.csv")); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return e
}

// ageSweep moves the blackhole bookkeeping back in time: every message first
// seen `by` ago, and the last sweep two hours ago (so the hourly gate is open).
func (e *m3Env) ageSweep(by time.Duration) {
	e.t.Helper()
	st, err := store.OpenExisting(e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	seen, err := st.BlackholeSeen(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for k, r := range seen {
		if strings.Contains(k, "m5-new") {
			continue // the one that is still young
		}
		r.FirstSeen = time.Now().Add(-by)
		seen[k] = r
	}
	err = st.Update(context.Background(), func(tx *store.Tx) error {
		if err := tx.ReplaceBlackholeSeen(seen); err != nil {
			return err
		}
		return tx.SetMeta(store.MetaSweptAt, strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10))
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestBlackholeRetentionSweep(t *testing.T) {
	e := newBlackholeEnv(t)
	e.deliver("SaneBlackHole", "spammer@bad.test", "<m5-old@t>")
	e.deliver("SaneBlackHole", "keeper@bad.test", "<m5-flagged@t>", imap.FlagFlagged)
	e.deliver("SaneBlackHole", "fresh@bad.test", "<m5-new@t>")

	// 1. The first pass only starts the clocks.
	out, err := e.run("once")
	if err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	if e.has("Trash", "<m5-old@t>") || strings.Contains(out, "from SaneBlackHole to Trash") {
		t.Fatalf("nothing is due on the first pass:\n%s", out)
	}

	// 2. A month later, a dry run says what would happen and changes nothing.
	e.ageSweep(40 * 24 * time.Hour)
	out, err = e.run("once", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	t.Logf("once --dry-run:\n%s", out)
	if !strings.Contains(out, "would move spammer@bad.test: SaneBlackHole -> Trash") ||
		!strings.Contains(out, "would move 1 message(s) from SaneBlackHole to Trash") ||
		strings.Contains(out, "fresh@bad.test") || strings.Contains(out, "keeper@bad.test: SaneBlackHole") {
		t.Errorf("dry-run output wrong:\n%s", out)
	}
	if !e.has("SaneBlackHole", "<m5-old@t>") || e.has("Trash", "<m5-old@t>") {
		t.Fatal("a dry run moved mail")
	}

	// 3. The real run moves the old message to Trash and nothing else.
	out, err = e.run("once")
	if err != nil {
		t.Fatalf("once: %v\n%s", err, out)
	}
	t.Logf("once:\n%s", out)
	if !strings.Contains(out, "moved 1 message(s) from SaneBlackHole to Trash (kept: 1 flagged)") {
		t.Errorf("summary missing:\n%s", out)
	}
	if !e.has("Trash", "<m5-old@t>") || e.has("SaneBlackHole", "<m5-old@t>") {
		t.Error("the old message should be in Trash")
	}
	if !e.has("SaneBlackHole", "<m5-flagged@t>") || !e.has("SaneBlackHole", "<m5-new@t>") {
		t.Error("the flagged and the young message must stay")
	}
	st, err := store.OpenExisting(e.db)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.Audit(context.Background())
	_ = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	purges := 0
	for _, r := range rows {
		if r.Action == store.ActionMove && r.FromFolder == "SaneBlackHole" && r.ToFolder == "Trash" {
			purges++
			if r.Status != store.AuditDone || r.DestUID == 0 || !strings.Contains(r.Reason, "retention") {
				t.Errorf("purge audit row = %+v", r)
			}
		}
	}
	if purges != 1 {
		t.Errorf("%d purge audit rows, want 1", purges)
	}

	// 4. undo brings it back, and it is not swept again.
	out, err = e.run("undo", "--since", "1h")
	if err != nil {
		t.Fatalf("undo: %v\n%s", err, out)
	}
	if !e.has("SaneBlackHole", "<m5-old@t>") || e.has("Trash", "<m5-old@t>") {
		t.Fatalf("undo did not restore it:\n%s", out)
	}
	e.ageSweep(90 * 24 * time.Hour)
	out, err = e.run("once")
	if err != nil {
		t.Fatalf("once after undo: %v\n%s", err, out)
	}
	if !e.has("SaneBlackHole", "<m5-old@t>") || e.has("Trash", "<m5-old@t>") || strings.Contains(out, "trained") {
		t.Errorf("a restored message must stay, and undo must not train:\n%s", out)
	}
}
