package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/classify"
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

func TestEvalExperimentFlagsExist(t *testing.T) {
	cfg, _ := scanConfig(t)
	// A bad cutoff is reported before connecting; an unknown flag would be a cobra error.
	_, err := execEval(t, "--config", cfg, "--mixed-override", "--cutoff", "last-month")
	if err == nil || !strings.Contains(err.Error(), "--cutoff") {
		t.Errorf("err = %v, want the --cutoff error (so the experiment flag parsed)", err)
	}
	// The rejected experiments are gone.
	for _, gone := range []string{"--thread-first", "--only-strong", "--no-mixed-override"} {
		if _, err := execEval(t, "--config", cfg, gone); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s should no longer exist: %v", gone, err)
		}
	}
}

func TestEvalPolicyFromFlags(t *testing.T) {
	if p := (&evalFlags{mixedOverride: true}).policy(); !p.MixedOverride {
		t.Errorf("policy = %+v", p)
	}
	if (&evalFlags{}).policy() != (classify.Policy{}) {
		t.Error("no flags must give the default policy")
	}
}
