# M5: BlackHole retention

## Context
`docs/PLAN.md` says mail from a blocked sender "goes to Trash". You want it to stay in `+QuietBlackHole`
instead, so you can review it, and mail older than a configurable retention (default 30 days) to be moved
to Trash. Most of M5 already works. M3 files a blackholed sender's mail into the folder, and M4 training
means a drag into the folder blocks the sender. What's missing is the **retention sweep**.

Decided in conversation:
- Age counts from **when the message landed in the blackhole folder**, not from INTERNALDATE.
  A move keeps INTERNALDATE, so that date would cut the review window short.
- Trash is found through the `\Trash` special-use flag, with an optional override.
- Flagged mail is never swept.
- A purge you reversed with `undo` is never purged again.

## Design

**Config** (`internal/config/config.go`): a new `[blackhole]` section with two keys.
- `retention`, default `"30d"`, written in `ParseDuration` notation. `"off"` disables the sweep.
- `trash_folder`, default `""`, which means use the folder flagged `\Trash`.
- Validation:
  - `trash_folder` must not be INBOX or a configured `[[folder]]`.
  - The section is accepted when no blackhole folder is configured. It then does nothing.

**State**: schema v6 (`internal/store/migrations.go`) adds
`blackhole_seen(key TEXT PRIMARY KEY, first_seen INTEGER NOT NULL)`. Keys and times only.
The time of the last sweep goes in the existing `meta` table under `blackhole_swept_at`.
New store methods:
- `BlackholeSeen`
- `Tx.PutBlackholeSeen` and `Tx.DeleteBlackholeSeen`, or a replace-all
- `Meta` / `SetMeta`, generalising the current `account` query

**Sweep** (`internal/engine/blackhole.go`, new file). Like `train.go`, its first half is pure and its
second half does the I/O.
- Pure: `planSweep(headers, seen map[key]time, rescued set, now, retention)`. It returns:
  - the UIDs to purge
  - the new `seen` rows: a first sighting gets `now`, and an existing time is kept
  - the keys to drop, for messages no longer in the folder
  - counts of messages kept because they are flagged or were rescued
- `rescued` holds the keys of a done move from the blackhole folder to Trash that is now `undone`,
  taken from `st.Audit`.
- I/O: `sweepBlackhole(ctx, cfg, b, st, o, rep)` is called at the end of `Process`
  (`internal/engine/process.go:135`, after `processInbox`, under the pass lock). Steps:
  1. Skip it if there is no blackhole folder, retention is `off`, or the folder is missing on the server.
  2. Real runs only: skip it if `blackhole_swept_at` is less than 1h ago. A dry run always sweeps
     (read only) so you can see what is due.
  3. Find Trash: the override, else `\Trash` in `b.ListFolders`. This mirrors `sentFolder` in
     `internal/engine/learn.go:498`. If there is none, add a **warning** (not a refusal) and skip the
     sweep. The rest of the pass still runs.
  4. Read the whole folder with `b.FetchFrom(ctx, folder, 1, …)`, then call `planSweep`.
  5. Move each due message with a new helper that follows `fileMessage`'s pattern
     (`process.go:401`):
     - write an audit row as **`ActionMove`**, status `planned`, reason `retention: in <folder> longer than <30d>`
     - `b.Move`
     - `SetAuditResult` with the COPYUID destination
     - `ForgetMessage(key)`

     A failed move is counted and the sweep continues.
  6. In one transaction: update `blackhole_seen` and set `blackhole_swept_at = now`. A dry run writes nothing.

**Why reuse `ActionMove`?** The existing machinery then works unchanged:
- `undo --since` reverses purges, newest first. It chains, so a long window can undo the purge and then
  the original filing.
- `reconcile` settles a purge interrupted by a crash.
- `ownMoves` explains a restored arrival, so `undo` does not train.
- Dragging a message from Trash back to INBOX is read as a return. That pins the sender to INBOX,
  which is the natural way to unblock someone.

**Report**: `ProcessReport.Purged []MoveOutcome`. `once` (`cmd/quietbox/once.go:137`) and `run`
(`engine/run.go:119`) each print one line per message:
`moved N message(s) older than 30d from +QuietBlackHole to Trash (K flagged kept)`.
A dry run says `would move`. These rows are kept apart from `rep.Moves`, so the INBOX counts stay as
they are.

**Precision**: a message's clock starts at the first sweep that sees it. That is at most about an hour
after it arrives, assuming `run`'s passes (at least every 20 min) or a cron `once`. This goes in the docs.

## Files
- `internal/config/config.go`, `config_test.go`: the `[blackhole]` section, defaults, `off`, validation
- `internal/store/migrations.go`, `store.go` (or a new `blackhole.go`), and tests: v6, seen rows, meta
- `internal/engine/blackhole.go` (new), `blackhole_test.go` (table tests for `planSweep`, and pass tests
  with `mailtest.Fake`, which needs folders carrying `\Trash` in `Attrs`)
- `internal/engine/process.go`: call the sweep, and add `Purged` to the report
- `cmd/quietbox/once.go`, `engine/run.go`: report lines
- `cmd/quietbox/m5_itest_test.go` (new). If Dovecot 2.4.5 does not flag Trash with `\Trash` by default,
  `testdata/dovecot/99-test.conf` gets `mailbox Trash { special_use = \Trash }`. I'll check this first.
- Docs:
  - `docs/PLAN.md`: the folder table comment, M3's blackhole note, the State list (v6), and the M5
    section marked implemented with its verified and not-verified lists
  - `README.md`: the `[blackhole]` config
  - design doc `docs/superpowers/specs/2026-11-22-m5-blackhole-retention-design.md`
  - plan `docs/superpowers/plans/2026-11-22-m5-blackhole-retention.md`

## Order of work (TDD, small conventional commits)
1. The design doc and this plan as docs commits. Then a branch `m5-blackhole-retention`.
2. Config `[blackhole]`, tests first.
3. Store v6 plus meta helpers, tests first.
4. Pure `planSweep`, table tests:
   - first sighting
   - kept time
   - due and not due at the boundary
   - flagged kept
   - rescued kept
   - pruning of messages gone from the folder
   - duplicate keys
5. Sweep wiring in `Process`, pass tests with the fake:
   - purge, with the audit row written before the move
   - dry run writes nothing
   - the 1h gate
   - no Trash gives a warning
   - a failed move
   - `undo` restores, and the next sweep keeps the message
   - a reconcile of a planned purge
6. Report output in `once` and `run`.
7. Integration test and docs.

## Verification
- `make test` (with `-race`) and `make lint` must be clean.
- `make itest`, using a new M5 test against Dovecot:
  1. Seed mail into the blackhole folder.
  2. `once` records the messages and moves nothing.
  3. Backdate `blackhole_seen` and `blackhole_swept_at` through the store.
  4. `once --dry-run` lists the purge and the mailbox is unchanged.
  5. `once` puts the message in Trash, with a `done` move row.
  6. A flagged message stays.
  7. `undo --since 1h` brings it back.
  8. The next backdated `once` does not purge it again.
- A manual CLI demo against the docker server, with the command output shown. Backdating is done with
  `sqlite3` on the test database.
- Not verifiable here: Trash special-use on Fastmail and Gmail. On Gmail, moving into `[Gmail]/Trash`
  means Gmail deletes the message after its own 30 days. The provider's emptying of Trash is outside
  quietbox and goes in the docs.
