package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/config"
	"github.com/michele/quietbox/internal/engine"
)

func sendersEnv(t *testing.T) (cfgPath string) {
	t.Helper()
	cfgPath, _ = scanConfig(t)
	cfg, err := config.LoadOffline(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := openState(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	return cfgPath
}

func TestSendersSetUnsetAndList(t *testing.T) {
	cfg := sendersEnv(t)

	out, err := execRoot(t, "senders", "set", "News@Example.com", "SaneLater", "--config", cfg)
	if err != nil || !strings.Contains(out, "news@example.com -> SaneLater (trained)") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	// The existing listing still works, with its flags.
	out, err = execRoot(t, "senders", "--config", cfg)
	if err != nil || !strings.Contains(out, "news@example.com") || !strings.Contains(out, "trained") || !strings.Contains(out, "1 senders") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	out, err = execRoot(t, "senders", "--folder", "SaneLater", "--mixed", "--config", cfg)
	if err != nil || !strings.Contains(out, "0 senders") {
		t.Fatalf("list --folder --mixed: %v\n%s", err, out)
	}
	out, err = execRoot(t, "senders", "unset", "news@example.com", "--config", cfg)
	if err != nil || !strings.Contains(out, "news@example.com: trained route removed") {
		t.Fatalf("unset: %v\n%s", err, out)
	}
	out, err = execRoot(t, "senders", "unset", "news@example.com", "--config", cfg)
	if err != nil || !strings.Contains(out, "no trained route for news@example.com") {
		t.Fatalf("unset again: %v\n%s", err, out)
	}
	if _, err := execRoot(t, "senders", "set", "a@b.com", "Nowhere", "--config", cfg); err == nil || !strings.Contains(err.Error(), "SaneLater") {
		t.Fatalf("bad folder: err = %v", err)
	}
	if _, err := execRoot(t, "senders", "set", "a@b.com", "--config", cfg); err == nil {
		t.Fatal("set with one argument succeeded")
	}
	if _, err := execRoot(t, "senders", "extra", "--config", cfg); err == nil {
		t.Fatal("senders with a stray argument succeeded")
	}
}

func TestSendersSetWaitsForTheLock(t *testing.T) {
	cfgPath := sendersEnv(t)
	cfg, err := config.LoadOffline(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	held, err := engine.AcquireLock(engine.LockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		held()
	}()
	out, err := execRoot(t, "senders", "set", "a@b.com", "SaneLater", "--config", cfgPath)
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	if strings.Count(out, "waiting for another quietbox...") != 1 || !strings.Contains(out, "a@b.com -> SaneLater (trained)") {
		t.Fatalf("output:\n%s", out)
	}
}
