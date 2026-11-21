//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useIMAPLines adds lines to the [imap] table of the env's config, so the
// password comes from them and not from the environment.
func (e *m3Env) useIMAPLines(lines string) {
	e.t.Helper()
	data, err := os.ReadFile(e.cfg)
	if err != nil {
		e.t.Fatal(err)
	}
	out := strings.Replace(string(data), "tls = \"none\"\n", "tls = \"none\"\n"+lines+"\n", 1)
	if err := os.WriteFile(e.cfg, []byte(out), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.t.Setenv("QUIETBOX_IMAP_PASSWORD", "") // prove the password came from the new source
	e.t.Setenv("QUIETBOX_CONTAINER", "0")
}

func writePasswordFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "imap.pw")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestItestPasswordFile(t *testing.T) {
	e := newM3Env(t)
	e.useIMAPLines(`password_file = "` + writePasswordFile(t, "test\n") + `"`)
	out, err := e.run("once")
	if err != nil || !strings.Contains(out, "0 new") {
		t.Fatalf("once with password_file: %v\n%s", err, out)
	}
}

func TestItestPasswordCommand(t *testing.T) {
	e := newM3Env(t)
	e.useIMAPLines(`password_command = "printf 'test\n'"`)
	out, err := e.run("once")
	if err != nil || !strings.Contains(out, "0 new") {
		t.Fatalf("once with password_command: %v\n%s", err, out)
	}
}

func TestItestWrongPasswordFromFileNeverLeaks(t *testing.T) {
	e := newM3Env(t)
	e.useIMAPLines(`password_file = "` + writePasswordFile(t, "wrongpass\n") + `"`)
	out, err := e.run("once")
	if err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("expected a login failure, got %v", err)
	}
	if strings.Contains(err.Error(), "wrongpass") || strings.Contains(out, "wrongpass") {
		t.Error("the wrong password was echoed")
	}
}

// A long-running `run` re-reads the password file when it reconnects, so a
// rotated or corrected password needs no restart.
func TestItestRunRereadsPasswordFileOnReconnect(t *testing.T) {
	e := newM3Env(t)
	pwPath := writePasswordFile(t, "wrongpass\n")
	e.useIMAPLines(`password_file = "` + pwPath + `"`)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"run", "--config", e.cfg})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	defer cancel()

	time.Sleep(2 * time.Second) // the first connect fails with the wrong password
	if err := os.WriteFile(pwPath, []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.deliver("INBOX", "writer@news.test", "<pw-rotate@t>")
	deadline := time.Now().Add(40 * time.Second) // first retry after ~5s of backoff
	for time.Now().Before(deadline) && !e.has("SaneNews", "<pw-rotate@t>") {
		time.Sleep(500 * time.Millisecond)
	}
	if !e.has("SaneNews", "<pw-rotate@t>") {
		t.Fatalf("run did not pick up the corrected password file\n%s", out.String())
	}
	if strings.Contains(out.String(), "wrongpass") || strings.Contains(out.String(), "test\n") {
		t.Error("run output leaks a password")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run after cancel = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
}
