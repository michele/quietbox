//go:build unix

package main

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
	if s := <-got; s != "through the pipe" {
		t.Errorf("reader got %q", s)
	}
	if fi, err := os.Lstat(fifo); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the pipe was replaced: %v %v", fi, err)
	}
	assertOnlyEntries(t, dir, "report.pipe")
}
