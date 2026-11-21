package main

import (
	"bytes"
	"context"
	"errors"
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

// A typo is rejected at once, not after waiting for `run` to stop.
func TestSendersSetValidatesBeforeWaitingForTheLock(t *testing.T) {
	cfgPath := sendersEnv(t)
	cfg, err := config.LoadOffline(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	held, err := engine.AcquireLock(engine.LockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	for _, args := range [][]string{{"x@y.test", "BadFolder"}, {"nobody", "SaneLater"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		start := time.Now()
		out, err := execRootCtx(t, ctx, append([]string{"senders", "set"}, append(args, "--config", cfgPath)...)...)
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) || strings.Contains(out, "waiting") || time.Since(start) > 900*time.Millisecond {
			t.Errorf("set %v: err = %v after %v\n%s", args, err, time.Since(start), out)
		}
	}
}

// execRootCtx is execRoot with a context, so a command that would wait forever
// gives up when ctx is done.
func execRootCtx(t *testing.T, ctx context.Context, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), err
}
