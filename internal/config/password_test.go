package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withIMAP adds lines to the [imap] table of validBase.
func withIMAP(extra string) string {
	return strings.Replace(validBase, `username = "me@example.com"`, `username = "me@example.com"`+"\n"+extra, 1)
}

type fakeInfo struct {
	mode fs.FileMode
	dir  bool
}

func (f fakeInfo) Name() string       { return "f" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

type fakeFile struct {
	data string
	mode fs.FileMode
	dir  bool
}

type fakeFS map[string]fakeFile

// deps builds injected dependencies over the fake files and environment.
func (f fakeFS) deps(env map[string]string) passwordDeps {
	return passwordDeps{
		getenv: func(k string) string { return env[k] },
		stat: func(p string) (fs.FileInfo, error) {
			file, ok := f[p]
			if !ok {
				return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
			}
			return fakeInfo{mode: file.mode, dir: file.dir}, nil
		},
		readFile: func(p string) ([]byte, error) {
			file, ok := f[p]
			if !ok {
				return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
			}
			return []byte(file.data), nil
		},
		exists: func(p string) bool { _, ok := f[p]; return ok },
		run: func(context.Context, string) ([]byte, []byte, error) {
			return nil, nil, errors.New("no command expected")
		},
		commandTimeout: time.Second,
	}
}

func fileCfg(t *testing.T, extra string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(withIMAP(extra)), env(nil))
	if err != nil {
		t.Fatal(err)
	}
	cfg.baseDir = "/etc/quietbox"
	return cfg
}

func TestParsePasswordSources(t *testing.T) {
	if _, err := Parse([]byte(withIMAP("password_file = \"/p\"\npassword_command = \"echo x\"")), pw); err == nil ||
		!strings.Contains(err.Error(), "password_file") || !strings.Contains(err.Error(), "password_command") {
		t.Errorf("both sources: err = %v", err)
	}
	for _, extra := range []string{`password_file = "/p"`, `password_command = "echo x"`} {
		cfg, err := Parse([]byte(withIMAP(extra)), env(nil)) // no environment password at all
		if err != nil {
			t.Errorf("%s: %v", extra, err)
		} else if cfg.IMAP.Password != "" {
			t.Errorf("parsing must not resolve the password")
		}
	}
	if _, err := Parse([]byte(validBase), env(nil)); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Errorf("no source at all: err = %v", err)
	}
	if _, err := Parse([]byte(withIMAP(`password = "x"`)), pw); err == nil || !strings.Contains(err.Error(), "imap.password") {
		t.Errorf("a password key must stay forbidden: err = %v", err)
	}
}

func TestResolvePasswordFile(t *testing.T) {
	good := fakeFile{data: "s3cret\n", mode: 0o600}
	cases := []struct {
		name    string
		extra   string
		files   fakeFS
		env     map[string]string
		want    string
		wantErr string
	}{
		{"absolute", `password_file = "/run/pw"`, fakeFS{"/run/pw": good}, nil, "s3cret", ""},
		{"read-only owner", `password_file = "/run/pw"`, fakeFS{"/run/pw": {data: "x", mode: 0o400}}, nil, "x", ""},
		{"relative to the config folder", `password_file = "pw.txt"`, fakeFS{"/etc/quietbox/pw.txt": good}, nil, "s3cret", ""},
		{"home", `password_file = "~/pw"`, fakeFS{"/home/u/pw": good}, map[string]string{"HOME": "/home/u"}, "s3cret", ""},
		{"systemd credentials", `password_file = "${CREDENTIALS_DIRECTORY}/imap"`, fakeFS{"/run/creds/imap": good},
			map[string]string{"CREDENTIALS_DIRECTORY": "/run/creds"}, "s3cret", ""},
		{"unset variable", `password_file = "${CREDENTIALS_DIRECTORY}/imap"`, fakeFS{}, nil, "", "CREDENTIALS_DIRECTORY"},
		{"missing file", `password_file = "/run/none"`, fakeFS{}, nil, "", "/run/none"},
		{"directory", `password_file = "/run/d"`, fakeFS{"/run/d": {mode: 0o700, dir: true}}, nil, "", "not a regular file"},
		{"empty", `password_file = "/run/pw"`, fakeFS{"/run/pw": {data: " \n", mode: 0o600}}, nil, "", "empty"},
		{"several lines", `password_file = "/run/pw"`, fakeFS{"/run/pw": {data: "line1secret\nline2", mode: 0o600}}, nil, "", "one line"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := fileCfg(t, c.extra)
			cfg.IMAP.Password = "from-the-environment" // the file must win
			_, err := cfg.resolvePassword(context.Background(), c.files.deps(c.env))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Errorf("the error leaks file content: %v", err)
				}
				return
			}
			if err != nil || cfg.IMAP.Password != c.want {
				t.Fatalf("password = %q err = %v, want %q", cfg.IMAP.Password, err, c.want)
			}
		})
	}
}

func TestPasswordFilePermissions(t *testing.T) {
	loose := func(mode fs.FileMode) fakeFS { return fakeFS{"/run/pw": {data: "pw", mode: mode}} }
	const extra = `password_file = "/run/pw"`

	// Outside a container: group- or world-readable is refused.
	for _, mode := range []fs.FileMode{0o644, 0o640, 0o604, 0o660} {
		cfg := fileCfg(t, extra)
		_, err := cfg.resolvePassword(context.Background(), loose(mode).deps(nil))
		if err == nil || !strings.Contains(err.Error(), "chmod 600 /run/pw") {
			t.Errorf("mode %o outside a container: err = %v", mode, err)
		}
		if cfg.IMAP.Password != "" {
			t.Errorf("mode %o: the password was used despite the refusal", mode)
		}
	}

	// Inside a container (each way of detecting one): a warning instead.
	markers := map[string]func(fakeFS, map[string]string){
		"declared":    func(_ fakeFS, e map[string]string) { e["QUIETBOX_CONTAINER"] = "1" },
		"kubernetes":  func(_ fakeFS, e map[string]string) { e["KUBERNETES_SERVICE_HOST"] = "10.0.0.1" },
		"docker":      func(f fakeFS, _ map[string]string) { f["/.dockerenv"] = fakeFile{} },
		"podman":      func(f fakeFS, _ map[string]string) { f["/run/.containerenv"] = fakeFile{} },
		"declared-on": func(_ fakeFS, e map[string]string) { e["QUIETBOX_CONTAINER"] = "yes" },
	}
	for name, mark := range markers {
		t.Run(name, func(t *testing.T) {
			files, e := loose(0o644), map[string]string{}
			mark(files, e)
			cfg := fileCfg(t, extra)
			warns, err := cfg.resolvePassword(context.Background(), files.deps(e))
			if err != nil || cfg.IMAP.Password != "pw" {
				t.Fatalf("password = %q err = %v", cfg.IMAP.Password, err)
			}
			if len(warns) != 1 || !strings.Contains(warns[0], "/run/pw") || !strings.Contains(warns[0], "readable by other users") {
				t.Errorf("warnings = %v", warns)
			}
		})
	}

	// A private file in a container needs no warning.
	files, e := loose(0o600), map[string]string{"QUIETBOX_CONTAINER": "1"}
	if warns, err := fileCfg(t, extra).resolvePassword(context.Background(), files.deps(e)); err != nil || len(warns) != 0 {
		t.Errorf("private file in a container: warns=%v err=%v", warns, err)
	}
}

func TestInContainer(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		files fakeFS
		want  bool
	}{
		{"plain host", nil, fakeFS{}, false},
		{"declared", map[string]string{"QUIETBOX_CONTAINER": "1"}, fakeFS{}, true},
		{"kubernetes", map[string]string{"KUBERNETES_SERVICE_HOST": "x"}, fakeFS{}, true},
		{"docker marker", nil, fakeFS{"/.dockerenv": {}}, true},
		{"podman marker", nil, fakeFS{"/run/.containerenv": {}}, true},
		{"0 forces off even with a marker", map[string]string{"QUIETBOX_CONTAINER": "0"}, fakeFS{"/.dockerenv": {}}, false},
	}
	for _, c := range cases {
		d := c.files.deps(c.env)
		if got := inContainer(d.getenv, d.exists); got != c.want {
			t.Errorf("%s: inContainer = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestLoadAccountReadsPasswordFileFromDisk(t *testing.T) {
	dir := t.TempDir()
	pwPath := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pwPath, []byte("from-disk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "acct.toml")
	if err := os.WriteFile(cfgPath, []byte(withIMAP(`password_file = "pw.txt"`)), 0o600); err != nil { // relative: next to the config
		t.Fatal(err)
	}
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "")
	t.Setenv("QUIETBOX_CONTAINER", "0") // not a container, whatever machine this runs on
	cfg, err := LoadAccount(cfgPath, "")
	if err != nil || cfg.IMAP.Password != "from-disk" || len(cfg.PasswordWarnings) != 0 {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}

	// RefreshPassword picks up a rotated password without reloading the config.
	if err := os.WriteFile(pwPath, []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.RefreshPassword(context.Background()); err != nil || cfg.IMAP.Password != "rotated" {
		t.Fatalf("after refresh: %q %v", cfg.IMAP.Password, err)
	}

	// Loose permissions: refused on a plain host...
	if err := os.Chmod(pwPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAccount(cfgPath, ""); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("loose file on a plain host: err = %v", err)
	}
	// ... and a warning carried on the config inside a container.
	t.Setenv("QUIETBOX_CONTAINER", "1")
	cfg, err = LoadAccount(cfgPath, "")
	if err != nil || len(cfg.PasswordWarnings) != 1 {
		t.Fatalf("in a container: warnings = %v err = %v", cfg.PasswordWarnings, err)
	}
}

func TestOfflineLoadNeverTouchesPasswordSources(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "a.toml")
	body := withIMAP("password_file = \"/does/not/exist\"")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAccountOffline(cfgPath, ""); err != nil {
		t.Errorf("offline load read the password source: %v", err)
	}
}

func cmdCfg(t *testing.T) *Config {
	t.Helper()
	return fileCfg(t, `password_command = "get-password"`)
}

// runner builds deps whose command returns the given output.
func runner(stdout, stderr string, err error) passwordDeps {
	d := fakeFS{}.deps(nil)
	d.run = func(context.Context, string) ([]byte, []byte, error) { return []byte(stdout), []byte(stderr), err }
	return d
}

func TestResolvePasswordCommand(t *testing.T) {
	cfg := cmdCfg(t)
	cfg.IMAP.Password = "from-the-environment"
	if _, err := cfg.resolvePassword(context.Background(), runner("pw-from-cmd\n", "", nil)); err != nil || cfg.IMAP.Password != "pw-from-cmd" {
		t.Fatalf("password = %q err = %v", cfg.IMAP.Password, err)
	}
}

func TestPasswordCommandErrorsNeverLeakStdout(t *testing.T) {
	cases := []struct {
		name    string
		d       passwordDeps
		wantErr string
	}{
		{"failure shows status and stderr", runner("LEAKYOUT", "gpg: decryption failed", errors.New("exit status 3")), "gpg: decryption failed"},
		{"failure shows the exit status", runner("LEAKYOUT", "", errors.New("exit status 3")), "exit status 3"},
		{"empty output", runner(" \n", "", nil), "empty"},
		{"several lines", runner("LEAKYOUT\nmore", "", nil), "one line"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := cmdCfg(t)
			_, err := cfg.resolvePassword(context.Background(), c.d)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
			if strings.Contains(err.Error(), "LEAKY") || cfg.IMAP.Password != "" {
				t.Errorf("stdout leaked or the password was set despite the error: %v / %q", err, cfg.IMAP.Password)
			}
		})
	}
}

func TestPasswordCommandStderrIsTruncated(t *testing.T) {
	_, err := cmdCfg(t).resolvePassword(context.Background(), runner("", strings.Repeat("x", 10000), errors.New("exit status 1")))
	if err == nil || len(err.Error()) > 600 {
		t.Fatalf("error is %d bytes long", len(err.Error()))
	}
}

func TestRealPasswordCommand(t *testing.T) {
	run := func(command string, timeout time.Duration) (string, error) {
		cfg := fileCfg(t, "password_command = "+quoteTOML(command))
		d := realPasswordDeps()
		d.commandTimeout = timeout
		_, err := cfg.resolvePassword(context.Background(), d)
		return cfg.IMAP.Password, err
	}
	if got, err := run(`printf 'hunter2\n'`, 5*time.Second); err != nil || got != "hunter2" {
		t.Errorf("printf: %q %v", got, err)
	}
	_, err := run(`echo oops >&2; exit 3`, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "oops") {
		t.Errorf("failing command: %v", err)
	}
	start := time.Now()
	_, err = run(`sleep 10`, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 5*time.Second {
		t.Errorf("hanging command: err = %v after %s", err, time.Since(start))
	}
}

func quoteTOML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
