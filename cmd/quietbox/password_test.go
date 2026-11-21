package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// passwordFileConfig writes a config that takes its password from a file with
// the given mode, and a server on a closed port so a command that gets as far as
// connecting fails with a connection error.
func passwordFileConfig(t *testing.T, mode os.FileMode) string {
	t.Helper()
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "")
	dir := t.TempDir()
	pwPath := filepath.Join(dir, "imap.pw")
	if err := os.WriteFile(pwPath, []byte("file-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pwPath, mode); err != nil { // WriteFile is subject to the umask
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`
[imap]
host = "127.0.0.1"
port = 1
tls = "none"
username = "me@example.test"
password_file = %q

[state]
path = %q

[[folder]]
name = "INBOX"
type = "important"
[[folder]]
name = "SaneLater"
type = "later"
`, pwPath, filepath.Join(dir, "state", "q.db"))
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// Every command that loads the config shows the warning, not only `check`.
func TestPasswordFileWarningOnEveryCommand(t *testing.T) {
	cfg := passwordFileConfig(t, 0o644)
	t.Setenv("QUIETBOX_CONTAINER", "1")
	commands := [][]string{
		{"check"}, {"init", "--dry-run"}, {"scan"}, {"eval"}, {"once"}, {"run"}, {"undo", "--since", "1h"},
	}
	for _, args := range commands {
		out, _ := execRoot(t, append(args, "--config", cfg)...)
		if !strings.Contains(out, "warning: password file") || !strings.Contains(out, "readable by other users") {
			t.Errorf("%v shows no password warning:\n%s", args, out)
		}
		if strings.Contains(out, "file-password") {
			t.Errorf("%v printed the password", args)
		}
	}
}

func TestPasswordFileRefusedOutsideAContainer(t *testing.T) {
	cfg := passwordFileConfig(t, 0o644)
	t.Setenv("QUIETBOX_CONTAINER", "0")
	_, err := execRoot(t, "check", "--config", cfg)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "file-password") {
		t.Error("the error leaks the password")
	}
}

func TestPasswordFilePrivateFileWorksSilently(t *testing.T) {
	cfg := passwordFileConfig(t, 0o600)
	t.Setenv("QUIETBOX_CONTAINER", "0")
	out, err := execRoot(t, "check", "--config", cfg)
	// It read the password and got as far as dialing port 1.
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("a private file should not warn:\n%s", out)
	}
}

// Commands that never connect do not read the password source at all.
func TestOfflineCommandsIgnorePasswordSources(t *testing.T) {
	cfg := passwordFileConfig(t, 0o644)
	t.Setenv("QUIETBOX_CONTAINER", "1")
	out, _ := execRoot(t, "senders", "--config", cfg)
	if strings.Contains(out, "warning") {
		t.Errorf("senders read the password source:\n%s", out)
	}
}
