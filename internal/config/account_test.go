package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidAccountName(t *testing.T) {
	for _, ok := range []string{"gmail", "a-b_1", "x", strings.Repeat("a", 64)} {
		if err := ValidAccountName(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "A", "Gmail", "../x", "a/b", `a\b`, "a.b", "a b", ".", "..", strings.Repeat("a", 65)} {
		if err := ValidAccountName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestConfigDirAndAccountPath(t *testing.T) {
	xdg := env(map[string]string{"XDG_CONFIG_HOME": "/cfg", "HOME": "/home/u"})
	if got := ConfigDir(xdg); got != "/cfg/quietbox" {
		t.Errorf("ConfigDir(xdg) = %q", got)
	}
	home := env(map[string]string{"HOME": "/home/u"})
	if got := ConfigDir(home); got != "/home/u/.config/quietbox" {
		t.Errorf("ConfigDir(home) = %q", got)
	}
	p, err := AccountConfigPath(xdg, "gmail")
	if err != nil || p != "/cfg/quietbox/gmail.toml" {
		t.Errorf("AccountConfigPath = %q, %v", p, err)
	}
	if _, err := AccountConfigPath(xdg, "../etc/passwd"); err == nil {
		t.Error("path traversal accepted")
	}
}

func TestPasswordEnvFor(t *testing.T) {
	cases := map[string]string{
		"":          "QUIETBOX_IMAP_PASSWORD",
		"gmail":     "QUIETBOX_IMAP_PASSWORD_GMAIL",
		"work-mail": "QUIETBOX_IMAP_PASSWORD_WORK_MAIL",
	}
	for account, want := range cases {
		if got := PasswordEnvFor(account); got != want {
			t.Errorf("PasswordEnvFor(%q) = %q, want %q", account, got, want)
		}
	}
}

func TestParseAccountPasswordPrecedence(t *testing.T) {
	both := env(map[string]string{"QUIETBOX_IMAP_PASSWORD": "generic", "QUIETBOX_IMAP_PASSWORD_GMAIL": "specific"})
	cfg, err := ParseAccount([]byte(validBase), both, "gmail")
	if err != nil || cfg.IMAP.Password != "specific" || cfg.Account != "gmail" {
		t.Fatalf("specific should win: %+v %v", cfg, err)
	}
	generic := env(map[string]string{"QUIETBOX_IMAP_PASSWORD": "generic"})
	if cfg, err = ParseAccount([]byte(validBase), generic, "gmail"); err != nil || cfg.IMAP.Password != "generic" {
		t.Fatalf("fallback to the generic variable: %+v %v", cfg, err)
	}
	_, err = ParseAccount([]byte(validBase), env(nil), "gmail")
	if err == nil || !strings.Contains(err.Error(), "QUIETBOX_IMAP_PASSWORD_GMAIL") || !strings.Contains(err.Error(), "(or QUIETBOX_IMAP_PASSWORD)") {
		t.Fatalf("error should name both variables: %v", err)
	}
	// Without an account nothing changes.
	if _, err := Parse([]byte(validBase), env(map[string]string{"QUIETBOX_IMAP_PASSWORD_GMAIL": "x"})); err == nil {
		t.Error("an account variable must not satisfy a config loaded without an account")
	}
}

func TestParseAccountStateDefault(t *testing.T) {
	g := env(map[string]string{"QUIETBOX_IMAP_PASSWORD": "pw", "XDG_STATE_HOME": "/state"})
	cfg, err := ParseAccount([]byte(validBase), g, "gmail")
	if err != nil || cfg.State.Path != "/state/quietbox/gmail.db" {
		t.Fatalf("state = %q, %v", cfg.State.Path, err)
	}
	// An explicit path always wins.
	explicit := "[state]\npath = \"/data/mine.db\"\n" + validBase
	if cfg, _ = ParseAccount([]byte(explicit), g, "gmail"); cfg.State.Path != "/data/mine.db" {
		t.Errorf("explicit path overridden: %q", cfg.State.Path)
	}
	// No account: the legacy default, so existing databases are not orphaned.
	if cfg, _ = Parse([]byte(validBase), g); cfg.State.Path != "/state/quietbox/quietbox.db" {
		t.Errorf("legacy default changed: %q", cfg.State.Path)
	}
}

func TestAccountID(t *testing.T) {
	cfg, err := Parse([]byte(strings.Replace(validBase, `host = "imap.example.com"`, `host = "IMAP.Example.com"`, 1)), pw)
	if err != nil {
		t.Fatal(err)
	}
	cfg.IMAP.Username = "Me@Example.com"
	if got := AccountID(cfg); got != "me@example.com@imap.example.com" {
		t.Errorf("AccountID = %q", got)
	}
}

func TestListAccounts(t *testing.T) {
	dir := t.TempDir()
	qdir := filepath.Join(dir, "quietbox")
	if err := os.MkdirAll(qdir, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(qdir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("gmail.toml", strings.Replace(validBase, "imap.example.com", "imap.gmail.com", 1))
	write("fastmail.toml", validBase)
	write("broken.toml", "this is not toml [")
	write("notes.txt", "ignored")
	g := env(map[string]string{"XDG_CONFIG_HOME": dir, "XDG_STATE_HOME": "/state"})

	got, err := ListAccounts(g)
	if err != nil || len(got) != 3 {
		t.Fatalf("accounts = %+v, %v", got, err)
	}
	names := []string{got[0].Name, got[1].Name, got[2].Name}
	if names[0] != "broken" || names[1] != "fastmail" || names[2] != "gmail" {
		t.Errorf("not sorted by name: %v", names)
	}
	if got[0].Err == nil {
		t.Error("broken file should carry its error")
	}
	if got[2].Host != "imap.gmail.com" || got[2].Username != "me@example.com" || got[2].StatePath != "/state/quietbox/gmail.db" || got[2].Err != nil {
		t.Errorf("gmail = %+v", got[2])
	}
	// A password is not needed to list.
	if got[1].Err != nil {
		t.Errorf("fastmail: %v", got[1].Err)
	}

	empty, err := ListAccounts(env(map[string]string{"XDG_CONFIG_HOME": t.TempDir()}))
	if err != nil || len(empty) != 0 {
		t.Errorf("missing dir: %+v, %v", empty, err)
	}
}
