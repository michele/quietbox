package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/engine"
)

func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func sampleProcessReport() *engine.ProcessReport {
	return &engine.ProcessReport{
		Fetched: 5, Skipped: 1, Stayed: 1,
		Moves: []engine.MoveOutcome{
			{From: "INBOX", To: "SaneNews", Sender: "writer@substack.test", Rule: "domain", Reason: "domain default", Status: engine.MoveDone},
			{From: "INBOX", To: "SaneBulk", Sender: "noreply@shop.test", Rule: "bulk", Reason: "noreply sender; order keyword", Status: engine.MoveFailed},
		},
		Failed: 1,
	}
}

func TestOnceWithoutDatabaseFailsBeforeConnecting(t *testing.T) {
	cfg, db := scanConfig(t)
	_, err := execRoot(t, "once", "--config", cfg)
	if err == nil || !strings.Contains(err.Error(), "quietbox init") {
		t.Fatalf("err = %v, want a hint to run quietbox init", err)
	}
	if _, statErr := os.Stat(db); statErr == nil {
		t.Error("once created a database")
	}
}

func TestWriteProcessReport(t *testing.T) {
	var buf bytes.Buffer
	if err := writeProcessReport(&buf, sampleProcessReport(), false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"writer@substack.test", "SaneNews", "noreply@shop.test", "failed", "1 failed", "5 new", "1 skipped (read, flagged, moved once before, moved back by you, or a copy of mail already seen)"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

func TestWriteProcessReportDryRun(t *testing.T) {
	rep := &engine.ProcessReport{Fetched: 1, Moves: []engine.MoveOutcome{
		{From: "INBOX", To: "SaneNews", Sender: "a@substack.test", Rule: "domain", Reason: "r", Status: engine.MoveWouldMove},
	}}
	var buf bytes.Buffer
	_ = writeProcessReport(&buf, rep, true)
	out := buf.String()
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "would move") || !strings.Contains(out, "nothing was changed") {
		t.Errorf("dry-run report = %s", out)
	}
}

func TestWriteProcessReportShowsWarnings(t *testing.T) {
	rep := &engine.ProcessReport{Warnings: []string{"INBOX was renumbered by the server"}}
	var buf bytes.Buffer
	_ = writeProcessReport(&buf, rep, false)
	if !strings.Contains(buf.String(), "warning: INBOX was renumbered") {
		t.Errorf("report = %s", buf.String())
	}
}

func TestWriteProcessReportShowsTraining(t *testing.T) {
	rep := &engine.ProcessReport{Trained: []engine.TrainOutcome{
		{Sender: "a@x.test", To: "SaneLater", Source: "move", Status: engine.TrainDone},
		{Sender: "b@x.test", From: "SaneNews", Route: "SaneNews", To: "INBOX", Source: "move", Status: engine.TrainDone},
		{Sender: "a@x.test", Route: "SaneLater", To: "SaneBulk", Source: "move", Status: engine.TrainDone},
	}}
	var buf bytes.Buffer
	_ = writeProcessReport(&buf, rep, false)
	out := buf.String()
	for _, want := range []string{
		"trained a@x.test: you moved a message to SaneLater\n",
		"trained b@x.test: you moved a message from SaneNews to INBOX\n",
		"trained a@x.test: you moved a message to SaneBulk (route was SaneLater)\n",
		"trained 2 sender(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}

	rep.Trained = rep.Trained[:1]
	rep.Trained[0].Status = engine.TrainWould
	buf.Reset()
	_ = writeProcessReport(&buf, rep, true)
	if out := buf.String(); !strings.Contains(out, "would train a@x.test: you moved a message to SaneLater") || !strings.Contains(out, "would train 1 sender(s)") {
		t.Errorf("dry-run report = %s", out)
	}
}

func TestWriteProcessReportShowsNotesWithoutFailing(t *testing.T) {
	rep := &engine.ProcessReport{Notes: []string{"training from moves starts now for SaneNews; earlier moves are not learned"}}
	var buf bytes.Buffer
	_ = writeProcessReport(&buf, rep, false)
	if !strings.Contains(buf.String(), "note: training from moves starts now for SaneNews") {
		t.Errorf("report = %s", buf.String())
	}
	if err := processReportError(rep); err != nil {
		t.Errorf("notes alone must not fail the run: %v", err)
	}
	rep.Warnings = []string{"INBOX was renumbered"}
	if err := processReportError(rep); err == nil {
		t.Error("a warning should still fail the run")
	}
}

func TestProcessReportError(t *testing.T) {
	if err := processReportError(&engine.ProcessReport{}); err != nil {
		t.Errorf("clean report: %v", err)
	}
	if err := processReportError(sampleProcessReport()); err == nil || !strings.Contains(err.Error(), "1 move") {
		t.Errorf("failed moves: %v", err)
	}
	if err := processReportError(&engine.ProcessReport{Warnings: []string{"w"}}); err == nil {
		t.Error("a warning should give a non-zero exit")
	}
}

func TestUndoRequiresSince(t *testing.T) {
	cfg, _ := scanConfig(t)
	_, err := execRoot(t, "undo", "--config", cfg)
	if err == nil || !strings.Contains(err.Error(), "since") {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoRejectsBadSince(t *testing.T) {
	cfg, _ := scanConfig(t)
	_, err := execRoot(t, "undo", "--config", cfg, "--since", "soon")
	if err == nil || !strings.Contains(err.Error(), "--since") {
		t.Fatalf("err = %v", err)
	}
}

func TestUndoWithoutDatabaseFailsBeforeConnecting(t *testing.T) {
	cfg, _ := scanConfig(t)
	_, err := execRoot(t, "undo", "--config", cfg, "--since", "1h")
	if err == nil || !strings.Contains(err.Error(), "quietbox init") {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteUndoReportShowsSkips(t *testing.T) {
	rep := &engine.UndoReport{Outcomes: []engine.UndoOutcome{
		{AuditID: 7, From: "SaneNews", To: "INBOX", Reason: "domain: default", At: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), Status: engine.UndoRestored},
		{AuditID: 6, From: "SaneBulk", To: "INBOX", Reason: "bulk: noreply", Status: engine.UndoSkippedGone},
		{AuditID: 5, From: "SaneLater", To: "INBOX", Reason: "later", Status: engine.UndoSkippedUnfindable},
	}}
	var buf bytes.Buffer
	if err := writeUndoReport(&buf, rep, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"restored", "no longer there", "cannot be found", "1 restored", "2 skipped"} {
		if !strings.Contains(out, want) {
			t.Errorf("undo report lacks %q:\n%s", want, out)
		}
	}
}

func TestUndoReportError(t *testing.T) {
	if err := undoReportError(&engine.UndoReport{Outcomes: []engine.UndoOutcome{{Status: engine.UndoRestored}, {Status: engine.UndoSkippedGone}}}); err != nil {
		t.Errorf("skips are not failures: %v", err)
	}
	if err := undoReportError(&engine.UndoReport{Outcomes: []engine.UndoOutcome{{Status: engine.UndoFailed}}}); err == nil {
		t.Error("a failed undo should give a non-zero exit")
	}
}

func TestParseLockTimeout(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"2m": 2 * time.Minute, "1s": time.Second, "90s": 90 * time.Second, "1m30s": 90 * time.Second,
		"1h": time.Hour, "1d": 24 * time.Hour,
	} {
		got, err := parseLockTimeout(in)
		if err != nil || got != want {
			t.Errorf("parseLockTimeout(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "0s", "-1s", "abc", "soon", "1x"} {
		if d, err := parseLockTimeout(bad); err == nil || !strings.Contains(err.Error(), "--lock-timeout") {
			t.Errorf("parseLockTimeout(%q) = %v, %v; want a --lock-timeout error", bad, d, err)
		}
	}
}

func TestLockTimeoutFlagDefaultsToTwoMinutes(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"once", "undo", "init"} {
		c, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		f := c.Flags().Lookup("lock-timeout")
		if f == nil || f.DefValue != "2m" {
			t.Errorf("%s --lock-timeout = %+v, want default 2m", name, f)
		}
	}
}

// A bad --lock-timeout is a usage error: it fails before opening the database
// or connecting (the config's server is unreachable and its database missing).
func TestLockTimeoutFlagRejectedBeforeAnything(t *testing.T) {
	for _, args := range [][]string{{"once"}, {"undo", "--since", "1h"}, {"init"}} {
		for _, bad := range []string{"0", "abc", "-5s"} {
			cfg, db := scanConfig(t)
			_, err := execRoot(t, append(args, "--config", cfg, "--lock-timeout", bad)...)
			if err == nil || !strings.Contains(err.Error(), "--lock-timeout") || strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("%v --lock-timeout %s: err = %v", args, bad, err)
			}
			if _, statErr := os.Stat(db); statErr == nil {
				t.Errorf("%v --lock-timeout %s created a database", args, bad)
			}
		}
	}
}

func TestWriteProcessReportShowsPurges(t *testing.T) {
	rep := &engine.ProcessReport{Purged: []engine.MoveOutcome{
		{From: "SaneBlackHole", To: "Trash", Sender: "spam@x.test", Rule: engine.RuleRetention, Status: engine.MoveDone},
	}}
	var buf bytes.Buffer
	_ = writeProcessReport(&buf, rep, false)
	for _, want := range []string{"moved spam@x.test: SaneBlackHole -> Trash", "moved 1 message(s) from SaneBlackHole to Trash"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, buf.String())
		}
	}
	rep.Purged[0].Status = engine.MoveWouldMove
	buf.Reset()
	_ = writeProcessReport(&buf, rep, true)
	if !strings.Contains(buf.String(), "would move 1 message(s) from SaneBlackHole to Trash") {
		t.Errorf("dry-run report = %s", buf.String())
	}
}
