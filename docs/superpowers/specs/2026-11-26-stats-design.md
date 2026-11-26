# `quietbox stats`: design (short)

## Intent

Show, from what quietbox already recorded, how much it is doing for the user: an estimate of
time saved, where mail goes, and how often it gets it wrong. Also a way to watch trust grow
(or not) while the tool replaces SaneBox.

## Goals

- `quietbox stats [--since DUR]` (default `30d`; same duration notation as the rest of the CLI).
- Time saved: filed messages x `[stats] seconds_per_message` (default 5), always labelled an estimate.
- Breakdown per destination folder (count, share), split by the rule that decided it (`sender`, `sent_to`, `thread`,
  `domain`, `newsletter`, `bulk`, `later`). Retention purges to Trash are shown on the blackhole line, not counted as filed.
  (Settled while planning: the audit reason is stored as `"<rule>: <reason>"`, which gives the rule but not whether a
  `sender` route was learned or trained, so the split is by rule, not by route source.)
- Mistakes: corrected by you (drag back to INBOX, re-filed to another folder), `undo`ne moves, senders retrained.
  Error rate = corrections / filed.
- Optional `--json` for scripting.

## Non-goals / anti-goals

- No new tables, no schema migration, no counters. Query the existing tables only.
- No IMAP login, no lock, nothing written (like `senders` and `explain`).
- Never print addresses, subjects or bodies. Folders, counts and rules only.
- Not a replacement for `quietbox eval`: it cannot see mail that was wrongly hidden and never noticed.
  The output and README say the error rate is a floor.
- No web UI, no charts.

## Approaches

1. **Query existing tables** (`audit`, `training`, `senders`, `blackhole_seen`). No migration, works on every
   existing database. Limit: history is only what the audit log holds. **Chosen by the user**; revisit if it is slow.
2. Counters table updated per pass. Faster at scale, but a migration and a second source of truth that can drift
   from the audit log. Rejected for a personal mailbox.

## Design

- `internal/stats`: pure functions over plain row structs -> `Report`. Table-tested, no I/O.
- `internal/store`: one read-only method returning the rows for a window (audit moves, training events, retrained senders).
- `cmd/quietbox/stats.go`: parse flags, open state, call store + `stats.Compute`, print. Binds the account like other
  database commands (`openState`).
- **Filed** = `audit` rows with `action = 'move'`, `status = 'done'`, `at` in the window, excluding retention purges.
- **Correction** = a `training` row (`source = 'move'`) whose `message_key` matches a filed message and whose time is after
  that move. Drag back to INBOX vs re-filed elsewhere comes from the training row's `folder`. `undo` creates no
  training row, so it is counted separately from `audit.undone`.
- **Error rate** = corrections / filed (denominator = filed in the window; a correction counts if the original move is in the window).
- **Config:** `[stats] seconds_per_message` (positive integer, default 5), documented in the README and `docs/PLAN.md`.

## Left to the builder

- (Settled in the plan: the rule is the text before the first `": "` in `audit.reason`; no other column holds it.)
- Exact output layout (the sketch in the conversation is a guide, not a contract).
- Whether `--json` ships in the first cut.

## Done when (from CLAUDE.md)

Unit tests with `-race`, `make lint` clean, run end to end against the Docker test server with the command output shown
(a seeded run that files mail, a drag back, and an `undo`), README and `docs/PLAN.md` updated.
Not verifiable here: real Fastmail/Gmail numbers.
