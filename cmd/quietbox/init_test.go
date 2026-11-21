package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestParseNow(t *testing.T) {
	got, err := parseNow("2026-06-01")
	if err != nil || !got.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := parseNow(""); err != nil || !got.IsZero() {
		t.Errorf("empty should be zero, got %v, %v", got, err)
	}
	if _, err := parseNow("yesterday"); err == nil {
		t.Error("expected error")
	}
}

func TestAnswerYes(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "YES": true, " yes \n": true, "n": false, "": false, "maybe": false, "yy": false} {
		if got := answerYes(in); got != want {
			t.Errorf("answerYes(%q) = %v", in, got)
		}
	}
}

func TestConfirmerReadsAnswers(t *testing.T) {
	cmd := &cobra.Command{}
	var errOut bytes.Buffer
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader("y\nn\n"))
	ask := confirmer(cmd)
	if ask == nil {
		t.Fatal("an injected reader is interactive")
	}
	if ok, err := ask("A"); err != nil || !ok {
		t.Errorf("first answer = %v, %v", ok, err)
	}
	if ok, err := ask("B"); err != nil || ok {
		t.Errorf("second answer = %v, %v", ok, err)
	}
	if ok, err := ask("C"); err != nil || ok { // EOF means no
		t.Errorf("EOF answer = %v, %v", ok, err)
	}
	if !strings.Contains(errOut.String(), `Create folder "A"? [y/N]`) {
		t.Errorf("prompt = %q", errOut.String())
	}
}

func TestInitFreshFlagValidation(t *testing.T) {
	cfg, _ := scanConfig(t) // an unreachable server: these must fail before connecting
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"backlog needs fresh", []string{"init", "--config", cfg, "--backlog", "30d"}, "--backlog requires --fresh"},
		{"fresh excludes folder", []string{"init", "--config", cfg, "--fresh", "--folder", "SaneLater"}, "--fresh"},
		{"bad backlog duration", []string{"init", "--config", cfg, "--fresh", "--backlog", "soon"}, "--backlog"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := execRoot(t, c.args...)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
			if strings.Contains(err.Error(), "connect") {
				t.Errorf("reached the network: %v", err)
			}
		})
	}
}
