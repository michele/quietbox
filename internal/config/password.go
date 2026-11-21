package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The IMAP password never lives in the config file. It comes from, in order:
//   - password_file: a file holding it (Docker/Kubernetes secrets, systemd
//     LoadCredential=); it must not be readable by group or others, except inside
//     a container, where secrets are routinely mounted 0444/0644 and a warning is
//     shown instead;
//   - password_command: a shell command printing it (a keychain, pass, ...);
//   - otherwise the environment variables (see PasswordEnvFor).
// Nothing here ever logs or returns the password in an error.

// passwordDeps are the outside world as seen by password resolution; tests
// replace them.
type passwordDeps struct {
	getenv         func(string) string
	stat           func(string) (fs.FileInfo, error) // follows symlinks
	readFile       func(string) ([]byte, error)
	exists         func(string) bool
	run            func(ctx context.Context, command string) (stdout, stderr []byte, err error)
	commandTimeout time.Duration
}

func realPasswordDeps() passwordDeps {
	return passwordDeps{
		getenv:   os.Getenv,
		stat:     os.Stat,
		readFile: os.ReadFile,
		exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
		run:      runShell,
		// A keychain prompt or a slow vault is fine; a hang is not.
		commandTimeout: 30 * time.Second,
	}
}

func (c *Config) hasPasswordSource() bool {
	return c.IMAP.PasswordFile != "" || c.IMAP.PasswordCommand != ""
}

// ResolvePassword reads the password from password_file or password_command
// into c.IMAP.Password and returns warnings to show the user. With neither set
// it does nothing (the environment variable was read when parsing).
func (c *Config) ResolvePassword(ctx context.Context) ([]string, error) {
	return c.resolvePassword(ctx, realPasswordDeps())
}

// RefreshPassword re-reads the password source, so a rotated file or a changed
// keychain entry is picked up by a long-running process on its next reconnect.
func (c *Config) RefreshPassword(ctx context.Context) error {
	_, err := c.ResolvePassword(ctx)
	return err
}

func (c *Config) resolvePassword(ctx context.Context, d passwordDeps) ([]string, error) {
	var password string
	var warnings []string
	var err error
	switch {
	case c.IMAP.PasswordFile != "":
		password, warnings, err = readPasswordFile(c.IMAP.PasswordFile, c.baseDir, d)
	case c.IMAP.PasswordCommand != "":
		password, err = runPasswordCommand(ctx, c.IMAP.PasswordCommand, d)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.IMAP.Password = password
	return warnings, nil
}

// inContainer guesses whether we run in a container: QUIETBOX_CONTAINER set to
// 0, false, no or off says no, any other value says yes; otherwise Kubernetes
// or the Docker/Podman marker files. A heuristic, which is why it can be
// declared. (A "false" must never count as "yes": that would weaken the
// password file permission check.)
func inContainer(getenv func(string) string, exists func(string) bool) bool {
	if v := getenv("QUIETBOX_CONTAINER"); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "no", "off":
			return false
		}
		return true
	}
	return getenv("KUBERNETES_SERVICE_HOST") != "" || exists("/.dockerenv") || exists("/run/.containerenv")
}

func readPasswordFile(path, baseDir string, d passwordDeps) (password string, warnings []string, err error) {
	path, err = expandPath(path, baseDir, d.getenv)
	if err != nil {
		return "", nil, fmt.Errorf("imap.password_file: %w", err)
	}
	info, err := d.stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("imap.password_file %s: no such file", path)
		}
		return "", nil, fmt.Errorf("imap.password_file %s: %w", path, err)
	}
	if info.IsDir() || !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("imap.password_file %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		msg := fmt.Sprintf("password file %s is readable by other users (mode %04o)", path, info.Mode().Perm())
		if !inContainer(d.getenv, d.exists) {
			return "", nil, fmt.Errorf("%s: run `chmod 600 %s`", msg, path)
		}
		warnings = append(warnings, msg+"; fine for a mounted container secret, not elsewhere")
	}
	data, err := d.readFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("imap.password_file %s: %w", path, err)
	}
	password, err = singleLine(string(data), "imap.password_file "+path)
	return password, warnings, err
}

// expandPath expands ~/ and $VAR / ${VAR} (an unset variable is an error, so a
// missing systemd credential is reported by name) and anchors a relative path at
// the config file's folder.
func expandPath(path, baseDir string, getenv func(string) string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("~ used but $HOME is not set")
		}
		path = home + strings.TrimPrefix(path, "~")
	}
	var missing []string
	path = os.Expand(path, func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("$%s is not set", missing[0])
	}
	if !filepath.IsAbs(path) && baseDir != "" {
		path = filepath.Join(baseDir, path)
	}
	return path, nil
}

// singleLine trims the value and insists on exactly one non-empty line, which
// catches pointing at the wrong file. It never echoes the value.
func singleLine(raw, what string) (string, error) {
	v := strings.TrimSpace(raw)
	switch {
	case v == "":
		return "", fmt.Errorf("%s is empty", what)
	case strings.ContainsAny(v, "\r\n"):
		return "", fmt.Errorf("%s must contain one line (the password)", what)
	}
	return v, nil
}

func runPasswordCommand(ctx context.Context, command string, d passwordDeps) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.commandTimeout)
	defer cancel()
	stdout, stderr, err := d.run(ctx, command)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("imap.password_command timed out after %s", d.commandTimeout)
		}
		msg := strings.TrimSpace(string(stderr))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		if msg != "" {
			msg = ": " + msg
		}
		return "", fmt.Errorf("imap.password_command failed (%v)%s", err, msg)
	}
	return singleLine(string(stdout), "the output of imap.password_command")
}

// limitWriter keeps at most max bytes but never blocks the writer.
type limitWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func runShell(ctx context.Context, command string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	out, errOut := &limitWriter{max: 64 << 10}, &limitWriter{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = out, errOut
	cmd.WaitDelay = time.Second // do not hang on a grandchild holding the pipes
	err = cmd.Run()
	return out.buf.Bytes(), errOut.buf.Bytes(), err
}
