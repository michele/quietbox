package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/stats"
	"github.com/michele/quietbox/internal/store"
)

// statsEnv makes a database with: three messages filed into SaneLater by the
// newsletter rule (k1..k3), a failed move, one old move (40 days ago) and one
// of k1 dragged back to INBOX by the user. It returns the config path and DB path.
func statsEnv(t *testing.T) (cfgPath, dbPath string) {
	t.Helper()
	cfgPath, dbPath = scanConfig(t)
	cfg, err := config.LoadOffline(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := openState(ctx, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now()
	add := func(key, status string, ago time.Duration) {
		t.Helper()
		_, err := st.AddAudit(ctx, store.AuditRow{
			Action: store.ActionMove, MessageKey: key, FromFolder: "INBOX", ToFolder: "SaneLater",
			Reason: "newsletter: newsletter detector (weak): list-id", At: now.Add(-ago), Status: status,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("k1", store.AuditDone, 3*time.Hour)
	add("k2", store.AuditDone, 3*time.Hour)
	add("k3", store.AuditDone, 3*time.Hour)
	add("kf", store.AuditFailed, 3*time.Hour)
	add("kold", store.AuditDone, 40*24*time.Hour)
	err = st.Update(ctx, func(tx *store.Tx) error {
		return tx.AddTraining(store.TrainingRow{At: now.Add(-time.Hour), Sender: "writer@example.test", Folder: "INBOX", Source: store.TrainMove, MessageKey: "k1"})
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfgPath, dbPath
}

func TestStatsText(t *testing.T) {
	cfg, _ := statsEnv(t)
	out, err := execRoot(t, "stats", "--config", cfg)
	if err != nil {
		t.Fatalf("stats: %v\n%s", err, out)
	}
	for _, want := range []string{"SaneLater", "Filed away  3 messages", "newsletter 3", "Corrected by you", "dragged back to INBOX 1", "est. time saved ~15 s"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "writer@example.test") {
		t.Errorf("output must not contain sender addresses:\n%s", out)
	}
}

func TestStatsJSON(t *testing.T) {
	cfg, _ := statsEnv(t)
	out, err := execRoot(t, "stats", "--json", "--config", cfg)
	if err != nil {
		t.Fatalf("stats --json: %v\n%s", err, out)
	}
	var r stats.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if r.Filed != 3 || r.Corrected != 1 || r.BackToInbox != 1 {
		t.Fatalf("report = %+v, want filed 3, corrected 1, back to INBOX 1", r)
	}
}

func TestStatsSinceExcludesOld(t *testing.T) {
	cfg, _ := statsEnv(t)
	out, err := execRoot(t, "stats", "--json", "--since", "60d", "--config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	var r stats.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Filed != 4 {
		t.Fatalf("60d: err %v, filed %d, want 4 (the old move counts)\n%s", err, r.Filed, out)
	}
	out, err = execRoot(t, "stats", "--json", "--since", "1d", "--config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Filed != 3 {
		t.Fatalf("1d: err %v, filed %d, want 3 (the old move does not)\n%s", err, r.Filed, out)
	}
}

func TestStatsNoDatabase(t *testing.T) {
	cfg, db := scanConfig(t)
	out, err := execRoot(t, "stats", "--config", cfg)
	if err == nil || !strings.Contains(err.Error(), "quietbox init") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if _, statErr := os.Stat(db); !os.IsNotExist(statErr) {
		t.Fatalf("stats must not create the database (stat: %v)", statErr)
	}
}

func TestStatsBadSince(t *testing.T) {
	cfg, _ := statsEnv(t)
	for _, v := range []string{"0d", "abc"} {
		if out, err := execRoot(t, "stats", "--since", v, "--config", cfg); err == nil {
			t.Errorf("--since %s: want an error, got\n%s", v, out)
		}
	}
}
