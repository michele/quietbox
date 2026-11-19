package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func execEval(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"eval"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestEvalFlagValidationHappensBeforeConnecting(t *testing.T) {
	cfg, _ := scanConfig(t) // IMAP port 1 is closed: a dial would say "refused"
	for name, args := range map[string][]string{
		"bad cutoff":  {"--cutoff", "last-month"},
		"bad history": {"--history", "ages"},
		"bad now":     {"--now", "yesterday"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := execEval(t, append([]string{"--config", cfg}, args...)...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "refused") || strings.Contains(err.Error(), "connect") {
				t.Errorf("flag errors must come before connecting: %v", err)
			}
		})
	}
}

func TestDefaultEvalCutoff(t *testing.T) {
	now := time.Date(2026, 6, 1, 15, 30, 0, 0, time.UTC)
	got := defaultEvalCutoff(now)
	if want := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("default cutoff = %v, want %v (60 days back, start of day)", got, want)
	}
}
