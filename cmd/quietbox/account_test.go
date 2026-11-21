package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/store"
)

// accountEnv points the config and state folders at temp dirs and clears the
// generic password so a test sees exactly the variable it sets.
func accountEnv(t *testing.T) (cfgDir, stateDir string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "")
	cfgDir = filepath.Join(root, "config", "quietbox")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return cfgDir, filepath.Join(root, "state", "quietbox")
}

// accountToml is a valid config whose server is a closed port, so a command
// that gets as far as connecting fails with a connection error.
func accountToml(host string, port int, user string) string {
	return `
[imap]
host = "` + host + `"
port = ` + strconv.Itoa(port) + `
tls = "none"
username = "` + user + `"

[[folder]]
name = "INBOX"
type = "important"
[[folder]]
name = "SaneLater"
type = "later"
`
}

func writeAccount(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountAndConfigTogetherFail(t *testing.T) {
	accountEnv(t)
	_, err := execRoot(t, "check", "--account", "gmail", "--config", "/somewhere/else.toml")
	if err == nil || !strings.Contains(err.Error(), "--account") || !strings.Contains(err.Error(), "--config") {
		t.Fatalf("err = %v, want one naming both flags", err)
	}
}

func TestAccountRejectsBadNames(t *testing.T) {
	accountEnv(t)
	for _, name := range []string{"../x", "a/b", "A", ""} {
		args := []string{"check", "--account", name}
		if name == "" {
			continue // an empty --account means "not given"
		}
		if _, err := execRoot(t, args...); err == nil || !strings.Contains(err.Error(), "invalid account name") {
			t.Errorf("--account %q: err = %v", name, err)
		}
	}
}

func TestAccountUnknownNamesThePathItLookedAt(t *testing.T) {
	cfgDir, _ := accountEnv(t)
	_, err := execRoot(t, "check", "--account", "nosuch")
	want := filepath.Join(cfgDir, "nosuch.toml")
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to mention %s", err, want)
	}
}

func TestAccountMissingPasswordNamesTheVariables(t *testing.T) {
	cfgDir, _ := accountEnv(t)
	writeAccount(t, cfgDir, "gmail", accountToml("127.0.0.1", 1, "me@example.test"))
	_, err := execRoot(t, "check", "--account", "gmail")
	if err == nil || !strings.Contains(err.Error(), "QUIETBOX_IMAP_PASSWORD_GMAIL") {
		t.Fatalf("err = %v", err)
	}
}

func TestAccountLoadsItsOwnFileAndPassword(t *testing.T) {
	cfgDir, _ := accountEnv(t)
	writeAccount(t, cfgDir, "gmail", accountToml("127.0.0.1", 1, "me@example.test"))
	t.Setenv("QUIETBOX_IMAP_PASSWORD_GMAIL", "secret")
	_, err := execRoot(t, "check", "--account", "gmail")
	// It got as far as dialing port 1 of that file: config and password were found.
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("err = %v, want a connection error for 127.0.0.1:1", err)
	}
}

func TestOpenStoreBindsAndRefusesOtherAccount(t *testing.T) {
	_, stateDir := accountEnv(t)
	path := filepath.Join(stateDir, "shared.db")
	mk := func(user string) *config.Config {
		cfg, err := config.Parse([]byte(accountToml("127.0.0.1", 1, user)+"\n[state]\npath = \""+path+"\"\n"),
			func(string) string { return "pw" })
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	st, err := openState(context.Background(), mk("a@example.test"), true)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	st, err = openState(context.Background(), mk("a@example.test"), false)
	if err != nil {
		t.Fatalf("same account reopened: %v", err)
	}
	_ = st.Close()
	_, err = openState(context.Background(), mk("b@example.test"), false)
	if !errors.Is(err, store.ErrWrongAccount) || !strings.Contains(err.Error(), "[state] path") {
		t.Fatalf("other account: err = %v", err)
	}
	// The refused open must not leave the database locked or half-open.
	st, err = openState(context.Background(), mk("a@example.test"), false)
	if err != nil {
		t.Fatalf("reopen after refusal: %v", err)
	}
	_ = st.Close()
}

func TestAccountsCommand(t *testing.T) {
	cfgDir, stateDir := accountEnv(t)
	writeAccount(t, cfgDir, "gmail", strings.Replace(accountToml("imap.gmail.com", 993, "me@gmail.test"), `tls = "none"`, `tls = "implicit"`, 1))
	writeAccount(t, cfgDir, "broken", "not toml [")
	out, err := execRoot(t, "accounts")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gmail", "imap.gmail.com", "me@gmail.test", filepath.Join(stateDir, "gmail.db"), "broken", "invalid TOML"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestAccountsCommandWithNoneGivesAHint(t *testing.T) {
	cfgDir, _ := accountEnv(t)
	out, err := execRoot(t, "accounts")
	if err != nil || !strings.Contains(out, cfgDir) || !strings.Contains(out, "--account") {
		t.Fatalf("out = %q err = %v", out, err)
	}
}
