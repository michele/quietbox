# M5: blackhole retention: design

Status: approved by the user in conversation (retention idea and the age-from-arrival choice), then
as a plan. Follows M4 (`2026-11-21-m4-training-design.md`).

## Intent

`docs/PLAN.md` said mail from a blocked sender "goes to Trash". The user wants it to stay in
`+QuietBlackHole` so it can be looked through, and mail there to be moved to Trash once it is older
than a retention period (default 30 days, configurable). Blocking itself already works: M3 files a
blackholed sender's mail into the folder and M4 training makes a drag into it block the sender. M5
adds the retention sweep.

## Goals

- Mail in the blackhole folder longer than `[blackhole] retention` (default `30d`) is moved to Trash.
- The age counts from when the message landed in the folder, so a message blocked late still gets
  the whole review window.
- Every sweep move follows the safety rules: audit row first, `--dry-run`, reversible by `undo`,
  move to Trash and never expunge.

## Non-goals

- Emptying Trash. That is the provider's job; quietbox never expunges or deletes.
- Sweeping any folder but the blackhole one.
- A separate command. The sweep is part of the normal pass.

## Anti-goals

- Hiding mail for good. A swept message must stay recoverable (in Trash, and through `undo`).
- Purging something you flagged or restored.

## Approaches

1. **Age from `INTERNALDATE`.** Simple and stateless. A move keeps `INTERNALDATE`, so a message
   blocked on day 29 is trashed on day 30. Rejected: it defeats the review window.
2. **Age from arrival in the folder (chosen).** Quietbox records the first time a sweep sees each
   message. Needs a small table; the clock starts at most about an hour after arrival.
3. **A separate `quietbox purge` command run from cron.** Another thing to schedule and forget,
   and it would duplicate the lock and audit handling. Rejected.

## Design

- **Config**: `[blackhole]` with `retention` (default `"30d"`, `ParseDuration` notation, `"off"`
  disables) and `trash_folder` (default: the folder flagged `\Trash`). `trash_folder` may not be
  INBOX or a configured folder.
- **State**: schema v6, `blackhole_seen(key, first_seen)`; `blackhole_swept_at` in `meta`.
- **Sweep**: at the end of `Process`, under the pass lock. Real runs sweep at most hourly; a dry run
  always looks. It reads the folder, records first sightings, forgets messages that left, and moves
  each message past retention to Trash. Flagged messages and messages that `undo` restored are kept.
  Without a Trash folder it warns and skips; the rest of the pass is unaffected.
- **Audit**: a purge is an ordinary `move` row (reason `retention: ...`), so `undo`, `reconcile` and
  own-move recognition work unchanged. Dragging a swept message from Trash to INBOX is a return and
  unblocks the sender.
- **Report**: `ProcessReport.Purged`, printed by `once` and `run`, separate from INBOX moves.

## Constraints

- Pure planning (`planSweep`) with no I/O, table-tested; I/O in `sweepBlackhole`.
- No new dependencies.

## Left to the builder

Names of helpers, the exact report wording, and the hourly gate's storage details.

## Not verified (to be stated again when done)

Trash special-use on Fastmail and Gmail; behaviour on a very large blackhole folder (the folder is
read whole once an hour).
