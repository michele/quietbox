package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scanConfig writes a config whose state path does not exist and whose IMAP
// host is unreachable, so a test proves what happens before any connection.
func scanConfig(t *testing.T) (cfgPath, dbPath string) {
	t.Helper()
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "test")
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.toml")
	dbPath = filepath.Join(dir, "state", "q.db")
	cfg := fmt.Sprintf(`
[imap]
host = "127.0.0.1"
port = 1
tls = "none"
username = "me@example.test"

[state]
path = %q

[[folder]]
name = "INBOX"
type = "important"
[[folder]]
name = "SaneLater"
type = "later"
`, dbPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, dbPath
}

func execScan(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"scan"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestScanWithoutDatabaseFailsBeforeConnecting(t *testing.T) {
	cfg, db := scanConfig(t)
	_, err := execScan(t, "--config", cfg)
	if err == nil || !strings.Contains(err.Error(), "quietbox init") {
		t.Fatalf("err = %v, want a hint to run quietbox init", err)
	}
	// Port 1 is closed: reaching the dial would have produced a connection error.
	if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "refused") {
		t.Errorf("scan tried to connect before checking the database: %v", err)
	}
	if _, statErr := os.Stat(db); !os.IsNotExist(statErr) {
		t.Error("scan must not create the database")
	}
}

func TestScanFlagValidation(t *testing.T) {
	cfg, _ := scanConfig(t)
	for name, args := range map[string][]string{
		"bad since":      {"--since", "soonish"},
		"negative limit": {"--limit", "-1"},
		"bad now":        {"--now", "yesterday"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := execScan(t, append([]string{"--config", cfg}, args...)...)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), "quietbox init") {
				t.Errorf("flag errors must come before the database check: %v", err)
			}
		})
	}
}
