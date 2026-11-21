package main

import (
	"bytes"
	"strings"
	"testing"

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
