package engine

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/michele/quietbox/internal/config"
)

func TestLockExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := AcquireLock(path); err == nil || !strings.Contains(err.Error(), "another quietbox") {
		t.Fatalf("second acquire: err = %v", err)
	}
}

func TestLockReleasedAllowsReacquire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	release2()
}

func TestAcquireLockWaitWaitsThenSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		held()
	}()
	start := time.Now()
	release, err := AcquireLockWait(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("returned after %v, before the holder released", d)
	}
}

func TestAcquireLockWaitCallsOnWaitOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(1600 * time.Millisecond)
		held()
	}()
	var n atomic.Int32
	release, err := AcquireLockWait(context.Background(), path, func() { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	release()
	if n.Load() != 1 {
		t.Fatalf("onWait called %d times, want 1", n.Load())
	}

	// A free lock never calls onWait.
	n.Store(0)
	release, err = AcquireLockWait(context.Background(), path, func() { n.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	release()
	if n.Load() != 0 {
		t.Fatalf("onWait called %d times on a free lock", n.Load())
	}
}

func TestAcquireLockWaitCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = AcquireLockWait(ctx, path, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("cancel took %v", d)
	}
}

func TestDaemonLockSecondAcquireFailsFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock.run")
	release, err := AcquireDaemonLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	start := time.Now()
	_, err = AcquireDaemonLock(path)
	if err == nil || !strings.Contains(err.Error(), "another quietbox run is already running") || !strings.Contains(err.Error(), path) {
		t.Fatalf("second acquire: err = %v", err)
	}
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("did not fail fast: %v", d)
	}
}

func TestDaemonLockReleasedByRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock.run")
	release, err := AcquireDaemonLock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release2, err := AcquireDaemonLock(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	release2()
}

func TestDaemonAndPassLocksAreIndependent(t *testing.T) {
	dir := t.TempDir()
	daemon, pass := filepath.Join(dir, "q.db.lock.run"), filepath.Join(dir, "q.db.lock")
	d, err := AcquireDaemonLock(daemon)
	if err != nil {
		t.Fatal(err)
	}
	p, err := AcquireLock(pass)
	if err != nil {
		t.Fatalf("pass lock blocked by daemon lock: %v", err)
	}
	p()
	d()
	p, err = AcquireLock(pass)
	if err != nil {
		t.Fatal(err)
	}
	defer p()
	d, err = AcquireDaemonLock(daemon)
	if err != nil {
		t.Fatalf("daemon lock blocked by pass lock: %v", err)
	}
	d()
}

func TestDaemonLockPath(t *testing.T) {
	cfg := &config.Config{}
	cfg.State.Path = "/x/q.db"
	if got := DaemonLockPath(cfg); got != "/x/q.db.lock.run" {
		t.Fatalf("DaemonLockPath = %q", got)
	}
}

func TestAcquireLockTimeoutTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	start := time.Now()
	_, err = AcquireLockTimeout(context.Background(), path, 400*time.Millisecond, nil)
	if err == nil || !strings.Contains(err.Error(), "after waiting") || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d < 350*time.Millisecond || d > 1500*time.Millisecond {
		t.Fatalf("timed out after %v", d)
	}
	// The failed attempt must not leave the lock held or leak state.
	held()
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("acquire after timeout: %v", err)
	}
	release()
}

func TestAcquireLockTimeoutSucceedsWhenReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		held()
	}()
	release, err := AcquireLockTimeout(context.Background(), path, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestAcquireLockTimeoutCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err = AcquireLockTimeout(ctx, path, 10*time.Second, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestAcquireLockTimeoutCancelWinsOverTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db.lock")
	held, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 50; i++ {
		_, err = AcquireLockTimeout(ctx, path, time.Nanosecond, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: err = %v, want context.Canceled", i, err)
		}
	}
}

const lockHelperEnv = "QUIETBOX_LOCK_HELPER_DIR"

// TestLockHelperProcess is not a test: the SIGKILL test re-executes the test
// binary with lockHelperEnv set, and this function then takes both locks,
// reports it on stdout and waits to be killed.
func TestLockHelperProcess(t *testing.T) {
	dir := os.Getenv(lockHelperEnv)
	if dir == "" {
		t.Skip("helper process only")
	}
	// Keep the release closures (and so the open files) alive until the end,
	// so no finalizer can close a descriptor and drop a flock early.
	relDaemon, err := AcquireDaemonLock(filepath.Join(dir, "q.db.lock.run"))
	if err != nil {
		t.Fatal(err)
	}
	defer relDaemon()
	relPass, err := AcquireLock(filepath.Join(dir, "q.db.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer relPass()
	_, _ = os.Stdout.WriteString("locked\n")
	time.Sleep(time.Minute)
}

func TestLocksAreNotStrandedBySIGKILL(t *testing.T) {
	dir := t.TempDir()
	daemon, pass := filepath.Join(dir, "q.db.lock.run"), filepath.Join(dir, "q.db.lock")

	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() }) // never leave the child behind

	locked := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if sc.Text() == "locked" {
				locked <- true
				return
			}
		}
		locked <- false
	}()
	select {
	case ok := <-locked:
		if !ok {
			t.Fatal("helper exited without taking the locks")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("helper did not report locked in time")
	}

	// While the child lives, both locks are held.
	if r, err := AcquireDaemonLock(daemon); err == nil {
		r()
		t.Fatal("daemon lock acquired while the child holds it")
	} else if !strings.Contains(err.Error(), "another quietbox") {
		t.Fatalf("daemon lock: unexpected error %v", err)
	}
	if r, err := AcquireLock(pass); err == nil {
		r()
		t.Fatal("pass lock acquired while the child holds it")
	} else if !strings.Contains(err.Error(), "another quietbox") {
		t.Fatalf("pass lock: unexpected error %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	// The kernel dropped both locks with the process: no cleanup ran, none is needed.
	r, err := AcquireDaemonLock(daemon)
	if err != nil {
		t.Fatalf("daemon lock stranded after SIGKILL: %v", err)
	}
	r()
	r, err = AcquireLock(pass)
	if err != nil {
		t.Fatalf("pass lock stranded after SIGKILL: %v", err)
	}
	r()
	for _, p := range []string{daemon, pass} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("lock file %s: %v", p, err)
		}
	}
}
