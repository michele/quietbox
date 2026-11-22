# `run` releases the lock between passes: design

Status: approved by the user in conversation (design and the "no stranded lock" requirement).
Follows M4 (`docs/superpowers/specs/2026-11-21-m4-training-design.md`); M3 introduced the lock.

## Intent

`quietbox run` holds the instance lock for its whole life (M3). Consequence: `senders set/unset`
wait until the daemon stops, `once`/`undo` refuse while it is up, and `init`/`learn` take no lock
at all. The user is fine with `run` releasing the lock while it idles, **as long as this opens no
race**, and wants a guarantee that **a dying process never leaves a stranded lock**.

## Goals

- `run` holds an exclusive lock only while a `Process` pass is executing.
- `senders set/unset`, `once`, `undo` and `init`/`learn` (real runs) get in between passes.
- Two `run` daemons on one mailbox stay impossible.
- A process that dies (crash, `kill -9`, power loss, SIGTERM mid-pass) never blocks anyone: no
  stale lock, no cleanup step, no PID files.

## Non-goals

- Network filesystems with unreliable `flock` (document the caveat: state directory should be
  local).
- Windows or other non-Unix platforms (the existing stub stays).
- Changing what a pass does or when passes happen.

## The race that the naive change would open

The lifetime lock is also what refuses a second `run`. If it is simply dropped while idle, a second
`run` could acquire it during the gap and two daemons would fight over one mailbox.

## Design: two locks, one fixed order

- **Daemon lock** `<state path>.lock.run`: taken by `run` at startup, fail-fast, held until `run`
  exits. Only `run` ever touches it. A second `run` fails with
  `another quietbox run is already running on this mailbox (lock file ...)`.
- **Pass lock** `<state path>.lock` (the existing one, `LockPath`): taken around each `Process`
  pass by `run` (waiting for it, not failing), and by `once`, `undo`, `senders set/unset` and
  real `init`/`learn` runs. Dry runs take none (unchanged).
- **Order**: daemon lock first, then pass lock. No code path takes them in the other order, so
  there is no deadlock. The pass lock is never held across an IMAP IDLE wait.

## Why it is race-safe (verified by reading the code before writing this)

- `Process` re-reads everything from SQLite at the start of each pass (knowledge, routes, cursors,
  audit log, `messages`), so whatever `set`, `undo`, `once` or `init` did in the gap is seen.
- Moves made in the gap by `once`/`undo` are explained by audit rows (own moves match by
  destination UID; `undo` restores by their `undo_move` row), so the daemon neither double-moves
  nor trains on them.
- `reconcile` of interrupted moves runs at the start of every pass under the pass lock, so a
  `planned` row is never in flight in another process while it is settled.
- SQLite runs in WAL mode with a 5 s busy timeout, so the daemon's unlocked `mailWaiting` read
  cannot conflict with a writer in another process.

## Process death

`flock(2)` locks belong to the open file description and the kernel drops them when the process
exits for any reason. The lock *files* stay on disk, empty and harmless: their existence means
nothing, only a held flock does. No PID, no timestamp, no "is it alive" heuristics are added. This is
a requirement with a test: a child process takes both locks, is killed with SIGKILL, and a parent
acquires both immediately.

## Waiting rules

- `run` waits for the pass lock with a context (cancel on SIGINT/SIGTERM), and says so via its
  progress output: `waiting for another quietbox... (lock file ...)` after a second, then
  `still waiting for the pass lock after <d> (lock file ...)` every 5 minutes (final wave: the
  wait is unbounded, so it must not be silent).
- `senders set/unset`: unchanged behaviour (wait, notice after 1 s, Ctrl-C) but now they wait at
  most one pass.
- `once`, `undo`, `init`/`learn` wait up to **2 minutes** (`--lock-timeout DURATION` to change, same
  format as other durations) then fail with
  `another quietbox is still running on this mailbox after waiting <d> (lock file ...)`. A bound
  matters for cron: a hung process must not queue up an endless pile of waiting jobs.

## Approaches considered

- **A. Keep the lifetime lock** (status quo): `set` waits until the daemon stops. Rejected by the user.
- **B. Release between passes with one lock**: opens the two-daemons race. Rejected.
- **C. Two locks, fixed order (chosen).**
- **D. Single lock plus a PID/lease file for the daemon**: stale-file handling is exactly the
  stranded-lock risk the user wants gone. Rejected.

## Testing

Unit: daemon lock (fail-fast, released on `release()`, second acquire refused), timeout variant,
SIGKILL child test, `Process` with `LockTimeout`. Integration (Dovecot): `senders set` completes
while `run` idles and the daemon applies the new route to the next delivery; second `run` refused
while the first idles; `once`/`undo` work while `run` idles with no double move; a concurrent stress
(`once` loop + `senders set/unset` loop + `run`) under `-race`.
