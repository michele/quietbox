# M3: Run for real: design

Status: approved in conversation, 2026-11-20. Milestone definition lives in `docs/PLAN.md`.

## Intent

quietbox has learned a mailbox (M1) and can classify INBOX mail in shadow mode (M2). M3 is the
first milestone that **moves mail**: `quietbox once` (cron), `quietbox run` (daemon with IDLE),
`--dry-run`, and `quietbox undo --since <duration>`. The aim is that SaneBox can be switched off
and the inbox stays trustworthy.

## Goals

- Move newly arrived INBOX mail to the folder `classify` picks, and nothing else.
- Every move is audited *before* it runs and can be reversed by `undo`.
- Work only on **new** mail, found through per-folder UID cursors, never by rescanning.
- One `engine.Process` pass shared by `once` and `run`.

## Non-goals

- Training from user moves, typed-folder cursors, `explain`, `senders set` (M4).
- Blackhole handling (M5).
- EMAILID keys: go-imap v2 beta.9 cannot fetch them. Keys stay Message-ID, then hash.
- Refreshing sender routes: they change only on `init`/`learn` until M4.
- Gmail/Outlook-specific behaviour. Outlook may require OAuth2, which is unsupported.

## Anti-goals

- Moving mail the user has already seen or flagged.
- Moving the same message twice, or re-moving something the user dragged back or `undo` restored.
- Any expunge or permanent delete (safety rule 1).

## Constraints

CLAUDE.md safety rules apply. The server must advertise **MOVE** (RFC 6851) and **UIDPLUS**
(COPYUID, needed by undo); otherwise quietbox refuses to run and says why. Gmail and Outlook
support for these is unverified: `quietbox check` shows the capabilities.

## Approaches

One sensible shape: a single `Process` pass called by both commands. A separate daemon code path
was rejected because it would duplicate the safety logic (audit, skip rules, cursors).

## Design

### Cursors
Table `cursors(folder, uidvalidity, next_uid)`. `init` records INBOX and Sent cursors, taking
UIDNEXT at the **start** of its scan so mail arriving during `init` is not lost. `init` moves
nothing. If a folder's UIDVALIDITY differs from the stored one, `Process` moves nothing, resets
the cursor and tells the user to re-run `init`.

### `engine.Process(ctx, opts{DryRun})`
1. Take a lock file next to the database (flock). A second instance exits with a clear message.
2. Reconcile leftover `planned` audit rows: look for the message in the destination folder and
   mark the row `done` or `failed`.
3. Verify UIDVALIDITY for INBOX and Sent.
4. **Sent:** fetch `UID >= cursor`; update `sent_to` and `sent_message_ids`; advance the cursor.
5. **INBOX:** fetch `UID >= cursor` with flags. For each message, skip if it is `\Seen` or
   `\Flagged` (configurable), or if its key already has a `move` audit row (any status, undone
   or not). Otherwise `classify.Classify`.
6. For each message that should leave INBOX: insert audit row `planned`; MOVE; update the row to
   `done` with the destination UIDVALIDITY and UID from COPYUID. A failure marks it `failed` and
   the pass continues.
7. Advance the INBOX cursor only after the batch.

`--dry-run` prints the plan, writes no audit rows, moves nothing and does not advance cursors.

### `run`
Loop: `Process`, then IDLE on INBOX (re-issued every 20 min), `Process` on wake-up. Reconnect with
backoff on connection errors. SIGINT/SIGTERM finishes the current pass and exits.

### `undo --since <duration> [--dry-run]`
Select `done`, not-undone `move` rows within the window, newest first. Find each message by
(dest UIDVALIDITY, dest UID); if UIDVALIDITY changed, search the destination by Message-ID. Move it
back to `from_folder`, mark the row undone and write an `undo_move` audit row. A message that is
no longer in the destination (the user moved it) is skipped and reported.

### Data and interface changes
- Migration 3: `audit.status` (`planned|done|failed`), `audit.dest_uidvalidity`, `audit.dest_uid`;
  table `cursors`.
- `mail.Header` gains `UID` and `Flags`. `mail.Backend` gains folder status, fetch-new,
  move, idle and find-for-undo. Folder mutation stays behind `engine`.
- `imapbackend` implements them and refuses servers without MOVE and UIDPLUS.

### Files
`internal/mail/mail.go`, `internal/imapbackend/backend.go`, `internal/store/{migrations,store}.go`,
`internal/engine/{process,undo}.go`, `internal/engine/learn.go` (cursors), `internal/config/config.go`
(skip-seen/flagged option), `cmd/quietbox/{once,run,undo}.go`, `docs/PLAN.md`, README, fake backend
in `internal/mail/mailtest`.

## Left to the builder

Names of config keys, backoff numbers, lock file location, exact audit status transitions on crash
reconciliation, and the shape of the fake backend.

## Verification

Unit tests for `Process`/`undo` against the fake backend (with `-race`), then integration tests on
the Dovecot server: `once` moves new mail, `--dry-run` moves nothing, a second `once` is a no-op,
a dragged-back message is not re-moved, `undo` restores, UIDVALIDITY change is refused.
Not verifiable here: SaneBox switched off and a week of real use.
