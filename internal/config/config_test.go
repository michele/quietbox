package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

const validBase = `
[imap]
host = "imap.example.com"
username = "me@example.com"

[[folder]]
name = "INBOX"
type = "important"

[[folder]]
name = "SaneLater"
type = "later"
`

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var pw = env(map[string]string{"QUIETBOX_IMAP_PASSWORD": "secret"})

func TestParseValid(t *testing.T) {
	cfg, err := Parse([]byte(validBase), pw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IMAP.Port != 993 || cfg.IMAP.TLS != TLSImplicit {
		t.Errorf("defaults: port=%d tls=%q", cfg.IMAP.Port, cfg.IMAP.TLS)
	}
	if cfg.IMAP.Password != "secret" {
		t.Errorf("password not taken from env")
	}
	targets := cfg.DetectionTargets()
	if targets[TypeLater] != "SaneLater" || targets[TypeImportant] != "INBOX" {
		t.Errorf("targets = %v", targets)
	}
}

func TestParseErrors(t *testing.T) {
	folder := func(name, typ, extra string) string {
		return "\n[[folder]]\nname = \"" + name + "\"\ntype = \"" + typ + "\"\n" + extra
	}
	imap := "[imap]\nhost = \"imap.example.com\"\nusername = \"me\"\n"
	cases := []struct {
		name string
		toml string
		env  func(string) string
		want string
	}{
		{"missing password", validBase, env(nil), "QUIETBOX_IMAP_PASSWORD"},
		{"password in file", strings.Replace(validBase, `username = "me@example.com"`, `username = "me"`+"\npassword = \"x\"", 1), pw, "password"},
		{"unknown key", validBase + "\n[extra]\nfoo = 1\n", pw, "extra"},
		{"unknown type", imap + folder("INBOX", "important", "") + folder("X", "later", "") + folder("Y", "weird", ""), pw, "weird"},
		{"duplicate name", imap + folder("INBOX", "important", "") + folder("L", "later", "") + folder("L", "bulk", ""), pw, "duplicate"},
		{"no important", imap + folder("L", "later", ""), pw, "important"},
		{"two important", imap + folder("INBOX", "important", "") + folder("VIP", "important", "") + folder("L", "later", ""), pw, "important"},
		{"important not INBOX", imap + folder("VIP", "important", "") + folder("L", "later", ""), pw, "INBOX"},
		{"no later", imap + folder("INBOX", "important", ""), pw, "later"},
		{"two blackhole", imap + folder("INBOX", "important", "") + folder("L", "later", "") + folder("B1", "blackhole", "") + folder("B2", "blackhole", ""), pw, "blackhole"},
		{"two detect", imap + folder("INBOX", "important", "") + folder("L1", "later", "detect = true\n") + folder("L2", "later", "detect = true\n"), pw, "detect"},
		{"detect on blackhole", imap + folder("INBOX", "important", "") + folder("L", "later", "") + folder("B", "blackhole", "detect = true\n"), pw, "blackhole"},
		{"no host", "[imap]\nusername = \"me\"\n" + folder("INBOX", "important", "") + folder("L", "later", ""), pw, "host"},
		{"no username", "[imap]\nhost = \"h\"\n" + folder("INBOX", "important", "") + folder("L", "later", ""), pw, "username"},
		{"bad tls", imap + "tls = \"bogus\"\n" + folder("INBOX", "important", "") + folder("L", "later", ""), pw, "tls"},
		{"plaintext remote", strings.Replace(imap, "imap.example.com", "mail.example.com", 1) + "tls = \"none\"\n" + folder("INBOX", "important", "") + folder("L", "later", ""), pw, "loopback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.toml), tc.env)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Errorf("error leaks password: %q", err)
			}
		})
	}
}

func TestPlaintextLoopbackAllowed(t *testing.T) {
	src := "[imap]\nhost = \"127.0.0.1\"\nport = 1143\nusername = \"me\"\ntls = \"none\"\n" + `
[[folder]]
name = "INBOX"
type = "important"
[[folder]]
name = "L"
type = "later"
`
	if _, err := Parse([]byte(src), pw); err != nil {
		t.Fatal(err)
	}
}

func TestDetectionTargetsAndWarnings(t *testing.T) {
	src := validBase + `
[[folder]]
name = "News"
type = "newsletter"

[[folder]]
name = "Bulk1"
type = "bulk"

[[folder]]
name = "Bulk2"
type = "bulk"

[[folder]]
name = "NewsA"
type = "newsletter"
detect = true
`
	// newsletter has News + NewsA, NewsA opts in; bulk has two with no opt-in.
	cfg, err := Parse([]byte(src), pw)
	if err != nil {
		t.Fatal(err)
	}
	targets := cfg.DetectionTargets()
	if targets[TypeNewsletter] != "NewsA" {
		t.Errorf("newsletter target = %q", targets[TypeNewsletter])
	}
	if _, ok := targets[TypeBulk]; ok {
		t.Errorf("bulk should have no target: %v", targets)
	}
	w := cfg.Warnings()
	if len(w) != 1 || !strings.Contains(w[0], "bulk") {
		t.Errorf("warnings = %v", w)
	}
}

func TestLearnDefaults(t *testing.T) {
	cfg, err := Parse([]byte(validBase), env(map[string]string{
		"QUIETBOX_IMAP_PASSWORD": "secret",
		"XDG_STATE_HOME":         "/state",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Me.Addresses; len(got) != 1 || got[0] != "me@example.com" {
		t.Errorf("me.addresses = %v", got)
	}
	l := cfg.Learn
	if l.InboxLookbackDur != 180*24*time.Hour || l.HalfLifeDur != 90*24*time.Hour {
		t.Errorf("durations = %v %v", l.InboxLookbackDur, l.HalfLifeDur)
	}
	if l.Window != 20 || l.MixedShare != 0.2 || l.SentOverrideShare != 0.75 ||
		l.DomainMinSenders != 3 || l.DomainShare != 0.8 {
		t.Errorf("learn defaults = %+v", l)
	}
	if cfg.State.Path != "/state/quietbox/quietbox.db" {
		t.Errorf("state path = %q", cfg.State.Path)
	}
}

func TestStatePathFallbacks(t *testing.T) {
	cfg, err := Parse([]byte(validBase), env(map[string]string{"QUIETBOX_IMAP_PASSWORD": "x", "HOME": "/h"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State.Path != "/h/.local/state/quietbox/quietbox.db" {
		t.Errorf("state path = %q", cfg.State.Path)
	}
}

func TestMeAddressesNormalised(t *testing.T) {
	src := validBase + "\n[me]\naddresses = [\"Me@Example.com\", \"Alias@Example.com\", \"me@example.com\"]\n"
	cfg, err := Parse([]byte(src), pw)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Me.Addresses; len(got) != 2 || got[0] != "me@example.com" || got[1] != "alias@example.com" {
		t.Errorf("addresses = %v", got)
	}
}

func TestLearnOverridesAndErrors(t *testing.T) {
	ok := validBase + "\n[learn]\ninbox_lookback = \"1y\"\nwindow = 5\nsent_folder = \"Sent Items\"\npersonal_domains = [\"Family.Test\"]\n"
	cfg, err := Parse([]byte(ok), pw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Learn.InboxLookbackDur != 365*24*time.Hour || cfg.Learn.Window != 5 ||
		cfg.Learn.SentFolder != "Sent Items" || cfg.Learn.PersonalDomains[0] != "family.test" {
		t.Errorf("learn = %+v", cfg.Learn)
	}
	bad := map[string]string{
		"inbox_lookback":      `inbox_lookback = "soon"`,
		"half_life":           `half_life = "0d"`,
		"window":              `window = 0`,
		"mixed_share":         `mixed_share = 1.5`,
		"sent_override_share": `sent_override_share = 0`,
		"domain_min_senders":  `domain_min_senders = 0`,
		"domain_share":        `domain_share = 2`,
		"nonsense":            `nonsense = 1`,
	}
	for key, line := range bad {
		_, err := Parse([]byte(validBase+"\n[learn]\n"+line+"\n"), pw)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
}

func TestLoadOfflineDoesNotNeedPassword(t *testing.T) {
	path := t.TempDir() + "/c.toml"
	if err := os.WriteFile(path, []byte(validBase), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PasswordEnv, "")
	if _, err := Load(path); err == nil {
		t.Fatal("Load must still require the password")
	}
	cfg, err := LoadOffline(path)
	if err != nil {
		t.Fatalf("LoadOffline: %v", err)
	}
	if cfg.State.Path == "" || len(cfg.Folders) != 2 {
		t.Errorf("cfg = %+v", cfg)
	}
	// Everything else is still validated.
	bad := strings.Replace(validBase, `type = "later"`, `type = "weird"`, 1)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOffline(path); err == nil {
		t.Error("LoadOffline must still validate folders")
	}
}

func TestRunDefaults(t *testing.T) {
	cfg, err := Parse([]byte(validBase), pw)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Run.SkipSeen || !cfg.Run.SkipFlagged {
		t.Errorf("run defaults = %+v, want both true", cfg.Run)
	}
}

func TestRunOverrides(t *testing.T) {
	cfg, err := Parse([]byte("[run]\nskip_seen = false\n"+validBase), pw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.SkipSeen || !cfg.Run.SkipFlagged {
		t.Errorf("run = %+v, want skip_seen=false skip_flagged=true", cfg.Run)
	}
}

func TestRunUnknownKeyRejected(t *testing.T) {
	_, err := Parse([]byte("[run]\nskip_everything = true\n"+validBase), pw)
	if err == nil || !strings.Contains(err.Error(), "run.skip_everything") {
		t.Fatalf("err = %v", err)
	}
}

func TestBlackholeDefaults(t *testing.T) {
	cfg, err := Parse([]byte(validBase), pw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Blackhole.Retention != "30d" || cfg.Blackhole.RetentionDur != 30*24*time.Hour {
		t.Errorf("retention = %q (%v), want 30d", cfg.Blackhole.Retention, cfg.Blackhole.RetentionDur)
	}
	if !cfg.Blackhole.SweepEnabled() {
		t.Error("the sweep should be on by default")
	}
	if cfg.Blackhole.TrashFolder != "" {
		t.Errorf("trash_folder = %q, want empty (use \\Trash)", cfg.Blackhole.TrashFolder)
	}
}

func TestBlackholeOverrides(t *testing.T) {
	cfg, err := Parse([]byte(validBase+"\n[blackhole]\nretention = \"2w\"\ntrash_folder = \"Deleted\"\n"), pw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Blackhole.RetentionDur != 14*24*time.Hour || cfg.Blackhole.TrashFolder != "Deleted" {
		t.Errorf("got %+v", cfg.Blackhole)
	}
}

func TestBlackholeRetentionOff(t *testing.T) {
	cfg, err := Parse([]byte(validBase+"\n[blackhole]\nretention = \"off\"\n"), pw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Blackhole.SweepEnabled() || cfg.Blackhole.RetentionDur != 0 {
		t.Errorf("off should disable the sweep: %+v", cfg.Blackhole)
	}
}

func TestBlackholeErrors(t *testing.T) {
	cases := []struct{ name, section, want string }{
		{"bad duration", "retention = \"soon\"", "blackhole.retention"},
		{"zero", "retention = \"0d\"", "blackhole.retention"},
		{"trash is INBOX", "trash_folder = \"INBOX\"", "blackhole.trash_folder"},
		{"trash is a configured folder", "trash_folder = \"SaneLater\"", "blackhole.trash_folder"},
		{"unknown key", "purge = true", "blackhole.purge"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(validBase+"\n[blackhole]\n"+c.section+"\n"), pw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
		})
	}
}
