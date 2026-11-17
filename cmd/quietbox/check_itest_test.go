//go:build integration

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestCheckAgainstTestServer(t *testing.T) {
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "test")
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"check", "--config", "../../testdata/config/itest.toml"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	// The capability list depends on the server build; compare the rest.
	_, rest, _ := strings.Cut(out.String(), "\n")
	want, err := os.ReadFile("../../testdata/golden/check.txt")
	if err != nil {
		t.Fatal(err)
	}
	if rest != string(want) {
		t.Errorf("output differs from golden.\n--- got ---\n%s\n--- want ---\n%s", rest, want)
	}
}

func TestCheckWrongPasswordDoesNotLeak(t *testing.T) {
	t.Setenv("QUIETBOX_IMAP_PASSWORD", "hunter2-wrong")
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-v", "check", "--config", "../../testdata/config/itest.toml"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected login failure")
	}
	if strings.Contains(err.Error(), "hunter2-wrong") || strings.Contains(out.String(), "hunter2-wrong") {
		t.Error("password leaked")
	}
}
