package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/classify"
	"github.com/michele/quietbox/internal/engine"
	"github.com/michele/quietbox/internal/mail"
	"github.com/michele/quietbox/internal/store"
)

func TestWriteExplanationNeverPrintsSubject(t *testing.T) {
	e := &engine.Explanation{
		Header:   mail.Header{From: "a@x.test", MessageID: "<a>", UID: 7, Subject: "TOPSECRET-SUBJECT"},
		Decision: classify.Decision{Folder: "SaneNews", Rule: classify.RuleSender, Reason: "you moved it", Signals: []string{"list-unsubscribe"}},
		Route:    &store.SenderRow{Address: "a@x.test", RouteFolder: "SaneNews", RouteSource: store.SourceTrained},
		Training: []store.TrainingRow{{Folder: "SaneNews", Source: store.TrainMove}},
	}
	var out bytes.Buffer
	writeExplanation(&out, "INBOX", e)
	s := out.String()
	for _, want := range []string{"rule:", "a@x.test", "would decide now", "SaneNews"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "TOPSECRET") {
		t.Errorf("output leaks the subject:\n%s", s)
	}
}

func TestExplainWithoutDatabaseFailsBeforeConnecting(t *testing.T) {
	cfg, _ := scanConfig(t)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"explain", "--config", cfg, "5"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "quietbox init") {
		t.Fatalf("err = %v, want a hint to run quietbox init", err)
	}
}

func TestExplainRejectsEmptyArgumentAndUIDZeroBeforeConnecting(t *testing.T) {
	cfg, _ := scanConfig(t) // no database: anything that got past parsing would ask for init
	for _, arg := range []string{"", "   ", "0", "<>", " < > "} {
		_, err := execRoot(t, "explain", "--config", cfg, arg)
		if err == nil || !strings.Contains(err.Error(), "UID") || strings.Contains(err.Error(), "quietbox init") {
			t.Errorf("explain %q: err = %v, want a usage error", arg, err)
		}
	}
}

func TestParseExplainArg(t *testing.T) {
	tests := []struct {
		arg  string
		uid  uint32
		mid  string
		fail bool
	}{
		{"42", 42, "", false},
		{"abc@x.test", 0, "<abc@x.test>", false},
		{" <abc@x.test> ", 0, "<abc@x.test>", false},
		{"0", 0, "", true},
		{"", 0, "", true},
		{"<>", 0, "", true},
	}
	for _, tt := range tests {
		uid, mid, err := parseExplainArg(tt.arg)
		if (err != nil) != tt.fail || uid != tt.uid || mid != tt.mid {
			t.Errorf("parseExplainArg(%q) = %d, %q, %v", tt.arg, uid, mid, err)
		}
	}
}

// Times print in local time with the zone, like `undo`.
func TestWriteExplanationPrintsLocalTimes(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("TST", 2*3600)
	defer func() { time.Local = saved }()
	at := time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC)
	e := &engine.Explanation{
		Header:   mail.Header{From: "a@x.test", MessageID: "<a>", UID: 7},
		Training: []store.TrainingRow{{Sender: "a@x.test", Folder: "SaneNews", Source: store.TrainMove, At: at}},
		Audit:    []store.AuditRow{{Action: store.ActionMove, FromFolder: "INBOX", ToFolder: "SaneNews", Status: store.AuditDone, At: at}},
	}
	var out bytes.Buffer
	writeExplanation(&out, "INBOX", e)
	s := out.String()
	if strings.Count(s, "2026-11-21 11:30 TST") != 2 || strings.Contains(s, "09:30") {
		t.Errorf("times not local with zone:\n%s", s)
	}
}
