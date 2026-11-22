//go:build unix

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWriteReportFileNamedPipe(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "report.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	got := make(chan string, 1)
	go func() {
		f, err := os.Open(fifo)
		if err != nil {
			got <- "open: " + err.Error()
			return
		}
		defer func() { _ = f.Close() }()
		b, _ := io.ReadAll(f)
		got <- string(b)
	}()
	if err := writeReportFile(fifo, false, writeString("through the pipe")); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != "through the pipe" {
			t.Errorf("reader got %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the pipe reader")
	}
	if fi, err := os.Lstat(fifo); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the pipe was replaced: %v %v", fi, err)
	}
	assertOnlyEntries(t, dir, "report.pipe")
}

// TestWriteReportFileDevStdoutRedirectedToFile re-executes the test binary with
// its stdout redirected to a regular file: --report /dev/stdout must append to
// that file, not truncate it or rename a temporary file over it.
func TestWriteReportFileDevStdoutRedirectedToFile(t *testing.T) {
	if os.Getenv("QUIETBOX_TEST_DEVSTDOUT_CHILD") == "1" {
		_, _ = os.Stdout.WriteString("earlier output\n")
		if err := writeReportFile("/dev/stdout", false, writeString("report\n")); err != nil {
			_, _ = os.Stderr.WriteString("child: " + err.Error() + "\n")
			os.Exit(3)
		}
		os.Exit(0)
	}
	if _, err := os.Stat("/dev/stdout"); err != nil {
		t.Skip("no /dev/stdout:", err)
	}
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.txt")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	child := exec.Command(os.Args[0], "-test.run=^TestWriteReportFileDevStdoutRedirectedToFile$")
	child.Env = append(os.Environ(), "QUIETBOX_TEST_DEVSTDOUT_CHILD=1")
	child.Stdout = out
	var stderr strings.Builder
	child.Stderr = &stderr
	if err := child.Run(); err != nil {
		t.Fatalf("child: %v\n%s", err, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil || string(got) != "earlier output\nreport\n" {
		t.Errorf("redirected stdout = %q, %v; want the earlier output followed by the report", got, err)
	}
	assertOnlyEntries(t, dir, "out.txt")
}
