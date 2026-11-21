# `run` releases the lock between passes: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `run` holds the pass lock only while a pass executes, a separate daemon lock keeps two `run`s impossible, and nothing can leave a stranded lock.

**Architecture:** Two flock files with a fixed order (daemon lock, then pass lock). `engine.Run` takes the daemon lock for life and the pass lock per `Process` call; `once`/`undo`/`init`/`learn` wait (bounded) for the pass lock; `senders set/unset` keep waiting without a bound.

**Tech Stack:** Go, `syscall.Flock`, cobra. No new dependency.

**Spec:** `docs/superpowers/specs/2026-11-21-run-idle-lock-design.md` (read it first).

## Global Constraints

- No PID files, timestamps or liveness heuristics: only held `flock`s mean "locked".
- Lock order is always daemon lock, then pass lock; only `run` takes the daemon lock.
- The pass lock is never held across `WaitForChange` (IDLE) or backoff sleeps.
- Dry runs take no lock (unchanged). Safety rules in CLAUDE.md unchanged (never expunge/delete mail; audit before mutation).
- Lock-wait default for `once`/`undo`/`init`/`learn` is 2 minutes; flag `--lock-timeout DURATION` (parsed by `config.ParseDuration`).
- Error texts: second daemon: `another quietbox run is already running on this mailbox (lock file <path>)`; timeout: `another quietbox is still running on this mailbox after waiting <d> (lock file <path>)`.
- Podman is the container runtime and is not on the default PATH: `export PATH=$PATH:/opt/podman/bin` before `make itest`.
- Done = `make test` (-race), `make lint`, `make itest` green, demo output shown, `docs/PLAN.md` and README updated. Commits conventional, ending with `Claude-Session: https://claude.ai/code/session_01CUS7msYe81mSFUyxHhH6Nj`. Do not tag.

## Review Focus

1. **Stranded lock after SIGKILL**: a child holding both locks, killed with `kill -9`, must not block a new acquirer (Task 1).
2. **Two daemons** while the first is idle between passes (Task 2).
3. **Lock leak on cancel**: cancelling the context while `run` waits for the pass lock, or mid-pass, releases everything; no fd leak across thousands of passes (Task 2).
4. **`once` during a pass** waits (bounded) and then proceeds or times out with the documented message; it never runs concurrently with a pass (Task 3).
5. **`init` during a pass** no longer rewrites cursors/`messages` under a running pass (Task 3).

---

## File Structure

- `internal/engine/lock.go`, `lock_other.go`: daemon lock + timeout variant. `internal/engine/lock_test.go`.
- `internal/engine/run.go`, `run_test.go`: per-pass locking.
- `internal/engine/process.go`, `undo.go`, `learn.go`: bounded wait for the pass lock (`LockTimeout`).
- `cmd/quietbox/once.go`, `undo.go`, `init.go`, `run.go`: `--lock-timeout`, wiring.
- `cmd/quietbox/m4_itest_test.go` (adapt), new `cmd/quietbox/lock_itest_test.go`.
- `docs/PLAN.md`, `README.md`.

---

### Task 1: lock primitives and the stranded-lock guarantee

**Files:** Modify `internal/engine/lock.go`, `internal/engine/lock_other.go`, `internal/engine/lock_test.go`.

**Interfaces (Produces):**
```go
func DaemonLockPath(cfg *config.Config) string // LockPath(cfg) + ".run"
func AcquireDaemonLock(path string) (release func(), err error) // fail-fast; error text per Global Constraints
func AcquireLockTimeout(ctx context.Context, path string, timeout time.Duration, onWait func()) (release func(), err error)
// AcquireLockWait with a deadline; timeout error text per Global Constraints; ctx cancel returns ctx.Err()
```
`AcquireLockWait` and `AcquireLockTimeout` share one poll loop (no copy-paste). Non-unix stubs return the existing "needs file locking" error.

- [ ] **Step 1: Failing tests** in `lock_test.go`: `TestDaemonLockSecondAcquireFailsFast` (message names `run`), `TestDaemonLockReleasedByRelease`, `TestDaemonAndPassLocksAreIndependent` (holding one does not block the other), `TestAcquireLockTimeoutTimesOut` (timeout 400 ms, error contains `after waiting` and the path), `TestAcquireLockTimeoutSucceedsWhenReleased`, `TestAcquireLockTimeoutCancel`, and **`TestLocksAreNotStrandedBySIGKILL`**: re-exec the test binary (`os.Args[0] -test.run=TestLockHelperProcess` with an env var) as a child that takes both locks, prints `locked` and sleeps; the parent waits for `locked`, asserts both acquisitions FAIL while the child lives, sends `SIGKILL`, waits for the child, then asserts `AcquireDaemonLock` and `AcquireLock` both succeed immediately, and that the lock files still exist (their existence is irrelevant).
- [ ] **Step 2:** run `go test ./internal/engine/ -run 'Lock' -race` → FAIL.
- [ ] **Step 3:** Implement. No PID/timestamp is ever written to the lock files.
- [ ] **Step 4:** PASS; `make lint` clean.
- [ ] **Step 5:** Commit `feat(engine): daemon lock, bounded lock wait, SIGKILL-proof locks`.

---

### Task 2: `run` locks per pass

**Files:** Modify `internal/engine/run.go`, `internal/engine/run_test.go`, `cmd/quietbox/run.go` (if the refusal text is produced there).

**Interfaces:** Consumes Task 1. `Run` acquires `AcquireDaemonLock(DaemonLockPath(cfg))` first (error returned unchanged, it is a refusal); `runConnected` wraps each `Process` call: `AcquireLockWait(ctx, LockPath(cfg), func(){ say("waiting for another quietbox...") })`, `Process(..., ProcessOptions{LockHeld: true, ...})`, release **immediately after Process returns** (before `say` output, `mailWaiting`, `WaitForChange`). A context cancel while waiting returns nil from `Run` as today; the pass lock is released on every path (use a small helper so `defer`/early returns cannot leak it).

- [ ] **Step 1: Failing tests** (existing run fixtures in `run_test.go`): `TestRunHoldsNoPassLockWhileIdle` (after the first pass finished and `run` is in `WaitForChange`, `AcquireLock(LockPath(cfg))` succeeds and releases); `TestRunHoldsDaemonLockForLife` (a second `Run` returns the daemon-lock refusal while the first idles; after the first is cancelled a new `Run` starts); `TestRunWaitsForPassLockHeldByAnotherProcess` (test holds the pass lock, `Run` starts, performs no pass until released, then does; progress contains `waiting for another quietbox`); `TestRunCancelWhileWaitingForPassLock` (returns nil promptly, daemon lock released, pass lock not held by `Run`); `TestRunReleasesPassLockBeforeIdle` using the fake's `WaitForChange` hook to assert the lock is free at that moment; `TestRunReleasesLocksOnProcessError`. Keep all existing run tests passing (update only assertions that depended on the lifetime lock and list them).
- [ ] **Step 2:** run `go test ./internal/engine/ -run Run -race` → FAIL.
- [ ] **Step 3:** Implement as described.
- [ ] **Step 4:** PASS; `make test`, `make lint`.
- [ ] **Step 5:** Commit `feat(run): hold the pass lock only while a pass executes`.

---

### Task 3: `once`, `undo`, `init`/`learn` wait (bounded) for the pass lock

**Files:** Modify `internal/engine/process.go`, `internal/engine/undo.go`, `internal/engine/learn.go`, `cmd/quietbox/once.go`, `cmd/quietbox/undo.go`, `cmd/quietbox/init.go` and their tests (e.g. `TestM3OnceRefusesAnotherInstance` in `cmd/quietbox/m3_itest_test.go`, engine lock tests).

**Interfaces:** `ProcessOptions`, `UndoOptions` and `LearnOptions` gain `LockTimeout time.Duration` (zero means 2 minutes) and use `AcquireLockTimeout` instead of `AcquireLock` when they take the pass lock themselves (not when `LockHeld`, not in dry runs). `Learn` (real runs only, not `--dry-run`) now takes the pass lock for the whole call (it took none before). Each of `once`, `undo`, `init` gets `--lock-timeout DURATION` (default `2m`, validated with `config.ParseDuration`; a non-positive value is an error). While waiting they print `waiting for another quietbox...` to stderr after 1 s.

- [ ] **Step 1: Failing tests:** `TestProcessWaitsForPassLockThenRuns` (a goroutine releases the lock after 500 ms; `Process` completes; no concurrent execution: assert via a fake `OnFetch` hook that the lock was held by `Process` at fetch time), `TestProcessLockTimeout` (LockTimeout 400 ms, lock held: error contains `after waiting`), same pair for `Undo`, and for `Learn` (`TestLearnTakesThePassLock`: a held lock blocks a real `Learn` until timeout, and a dry-run `Learn` does not block), plus command tests that `--lock-timeout 0` and `--lock-timeout abc` are rejected and that the flag reaches the engine. Update `TestM3OnceRefusesAnotherInstance` to the new semantics (with `--lock-timeout 1s` the command fails with the documented message after waiting).
- [ ] **Step 2:** run the focused tests → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `make test`, `make lint`.
- [ ] **Step 5:** Commit `feat: once, undo and init wait for the pass lock instead of failing`.

---

### Task 4: integration tests, stress test, docs, demo

**Files:** Modify `cmd/quietbox/m4_itest_test.go` (the part of `TestM4SendersSetAndUnset` that asserts `set` blocks while `run` is up must be inverted: `set` now completes while `run` idles); Create `cmd/quietbox/lock_itest_test.go` (same build tag and helpers as the M3/M4 integration tests); Modify `docs/PLAN.md`, `README.md`.

- [ ] **Step 1: Integration tests** (Dovecot): `TestLockSendersSetCompletesWhileRunIdles` (run up and idle; `senders set` returns within a few seconds; a new delivery from that sender is then filed by the new route by the SAME running daemon); `TestLockSecondRunRefusedWhileFirstIdles`; `TestLockOnceAndUndoWorkWhileRunIdles` (`once` files mail, the daemon does not move anything twice: assert the audit log has exactly one `move` row per message; `undo` restores and the daemon then neither re-files nor trains); `TestLockStress` (a `run` plus a loop of `once` and a loop of `senders set/unset` for ~5 s under `-race`; afterwards: every message is in exactly one folder, no duplicate audit `move` rows for one message key, no `planned` rows left, `make itest` output pristine); `TestLockKilledRunLeavesNoStrandedLock` (start the real built binary or the in-process runner as a subprocess, `SIGKILL` it while idle and once mid-pass if feasible, then `once` succeeds immediately). Deterministic: poll with deadlines, no flaky sleeps.
- [ ] **Step 2:** `export PATH=$PATH:/opt/podman/bin && make itest` → PASS; paste the tail.
- [ ] **Step 3: Docs.** `docs/PLAN.md`: replace "run holds the lock for its lifetime / once and undo fail while run is up / senders set waits while run is up" (M3 and M4 sections) with the two-lock design, `--lock-timeout`, the SIGKILL guarantee and the local-filesystem caveat for `flock`; README likewise (usage of `senders set` while `run` is up, `once`/`undo` while `run` is up, what happens when the process dies).
- [ ] **Step 4: Demo** against the docker server, output shown in the report: `run` in the background idling; `senders set` returning immediately; a second `run` refused; `once` while idle; `kill -9` of the `run` process followed by `once` succeeding immediately; `ls` of the two lock files after the kill (still present, harmless).
- [ ] **Step 5:** `make test`, `make lint`, `make itest` green; commit `docs: run releases the lock between passes`.

---

## Self-review

- **Spec coverage:** daemon lock (T1/T2), per-pass lock (T2), bounded waiting + flag (T3), `init`/`learn` locking (T3), SIGKILL guarantee (T1 unit, T4 integration), docs (T4).
- **Order check:** only `Run` takes the daemon lock, always before any pass lock; `once`/`undo`/`init`/`set` take only the pass lock.
- **Known soft spot:** `flock` semantics on network filesystems; documented, not solved.
