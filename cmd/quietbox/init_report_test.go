package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michele/quietbox/internal/engine"
)

func TestDefaultReportPath(t *testing.T) {
	cases := []struct{ state, want string }{
		{"/home/me/.local/state/quietbox/quietbox.db", "/home/me/.local/state/quietbox/quietbox.learn-report.csv"},
		{"/home/me/.local/state/quietbox/auralis.db", "/home/me/.local/state/quietbox/auralis.learn-report.csv"},
		{"/data/quietbox.db", "/data/quietbox.learn-report.csv"},
		{"/srv/mail/state", "/srv/mail/state.learn-report.csv"},
		{"/srv/mail/state.sqlite", "/srv/mail/state.sqlite.learn-report.csv"},
		{"/srv/my.db.d/q.db", "/srv/my.db.d/q.learn-report.csv"},
		{"quietbox.db", "quietbox.learn-report.csv"},
		{"/srv/state/", "/srv/state/quietbox.learn-report.csv"},
		{"/srv/state/.db", "/srv/state/quietbox.learn-report.csv"},
	}
	for _, c := range cases {
		if got := defaultReportPath(c.state); got != c.want {
			t.Errorf("defaultReportPath(%q) = %q, want %q", c.state, got, c.want)
		}
	}
}

func TestReportTargetFlag(t *testing.T) {
	state := filepath.Join("/x", "state", "acct.db")
	cases := []struct {
		name        string
		args        []string
		wantPath    string
		wantDefault bool
	}{
		{"not given: next to the database", nil, "/x/state/acct.learn-report.csv", true},
		{"explicit path", []string{"--report", "/elsewhere/r.csv"}, "/elsewhere/r.csv", false},
		{"explicit empty: skip", []string{"--report", ""}, "", false},
		{"explicit empty with =", []string{"--report="}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := newInitCmd(&options{})
			if err := cmd.ParseFlags(c.args); err != nil {
				t.Fatal(err)
			}
			report, _ := cmd.Flags().GetString("report")
			path, isDefault := reportTarget(cmd, report, state)
			if path != c.wantPath || isDefault != c.wantDefault {
				t.Errorf("reportTarget = (%q, %v), want (%q, %v)", path, isDefault, c.wantPath, c.wantDefault)
			}
		})
	}
}

func writeString(s string) func(io.Writer) error {
	return func(w io.Writer) error { _, err := io.WriteString(w, s); return err }
}

func TestWriteReportFileReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.csv")
	if err := os.WriteFile(path, []byte("old content that is longer than the new one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeReportFile(path, false, writeString("new\n")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new\n" {
		t.Fatalf("content = %q, %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	assertOnlyEntries(t, dir, "r.csv")
}

func TestWriteReportFileFailedWriteKeepsOldReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.csv")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	err := writeReportFile(path, false, func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if got, _ := os.ReadFile(path); string(got) != "old\n" {
		t.Errorf("old report was damaged: %q", got)
	}
	assertOnlyEntries(t, dir, "r.csv")
}

func TestWriteReportFileCreatesStateDirOnlyForDefault(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state", "quietbox")
	path := filepath.Join(stateDir, "quietbox.learn-report.csv")

	if err := writeReportFile(path, false, writeString("x")); err == nil {
		t.Fatal("an explicit path under a missing directory must fail, not create it")
	}
	if _, err := os.Stat(filepath.Join(base, "state")); !os.IsNotExist(err) {
		t.Fatalf("explicit path created parent directories (stat err = %v)", err)
	}

	if err := writeReportFile(path, true, writeString("x")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(stateDir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("state dir = %v, %v; want mode 0700", fi, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "x" {
		t.Errorf("content = %q", got)
	}
}

func TestWriteReportFileNonRegularTargetIsWrittenInPlace(t *testing.T) {
	if _, err := os.Stat(os.DevNull); err != nil {
		t.Skip("no", os.DevNull)
	}
	if err := writeReportFile(os.DevNull, false, writeString("x")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(os.DevNull); err != nil || fi.Mode().IsRegular() {
		t.Fatalf("%s was replaced: %v %v", os.DevNull, fi, err)
	}
}

func TestWriteReportFileFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.csv")
	link := filepath.Join(dir, "link.csv")
	if err := os.WriteFile(real, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := writeReportFile(link, false, writeString("new")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced by a file: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(real); string(got) != "new" {
		t.Errorf("target content = %q", got)
	}
	assertOnlyEntries(t, dir, "link.csv", "real.csv")
}

// A dangling symlink is replaced by the report: the user chose that path.
func TestWriteReportFileReplacesDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link.csv")
	if err := os.Symlink(filepath.Join(dir, "gone.csv"), link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if err := writeReportFile(link, false, writeString("new")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("link.csv = %v, %v; want a regular 0600 file", fi, err)
	}
	if got, _ := os.ReadFile(link); string(got) != "new" {
		t.Errorf("content = %q", got)
	}
	assertOnlyEntries(t, dir, "link.csv")
}

func TestEmitReportWarnsInsteadOfFailing(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"a directory":         dir,
		"under a missing dir": filepath.Join(dir, "missing", "r.csv"),
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errb bytes.Buffer
			cmd := newInitCmd(&options{})
			cmd.SetOut(&out)
			cmd.SetErr(&errb)
			emitReport(cmd, &engine.LearnReport{}, path, false)
			want := fmt.Sprintf("warning: could not write the report to %s: ", path)
			if !strings.HasPrefix(errb.String(), want) {
				t.Errorf("stderr = %q, want prefix %q", errb.String(), want)
			}
			if strings.Contains(out.String(), "Report written") {
				t.Errorf("stdout claims success: %q", out.String())
			}
		})
	}
	assertOnlyEntries(t, dir)
}

func TestEmitReportSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.csv")
	var out, errb bytes.Buffer
	cmd := newInitCmd(&options{})
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	emitReport(cmd, &engine.LearnReport{}, path, false)
	if out.String() != "\nReport written to "+path+"\n" || errb.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
	if raw, err := os.ReadFile(path); err != nil || !strings.HasPrefix(string(raw), "kind,") {
		t.Errorf("report = %q, %v", raw, err)
	}
}

// assertOnlyEntries fails when dir holds anything but the named entries (for
// example a temporary file left behind by an atomic write).
func assertOnlyEntries(t *testing.T, dir string, names ...string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Errorf("%s holds %v, want %v", dir, got, names)
	}
}
