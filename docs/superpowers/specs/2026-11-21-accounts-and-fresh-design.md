# Accounts and fresh-account init: design

Status: approved in conversation, 2026-11-21. Follows M3 (`2026-11-20-m3-run-design.md`).

## Intent

People will run quietbox on several mailboxes (a SaneBox-managed Fastmail account and a never-managed
Gmail one). Two things are missing: an easy way to run **one process per account** without collisions,
and a way to start sorting an account that was **never managed**, where the INBOX history says nothing
about where mail belongs.

## Goals

- `--account NAME` selects `NAME.toml` in the default config folder; `--config PATH` keeps working.
- Each account has its own config, database and lock file, and a database can never be used by the
  wrong account.
- `init --fresh [--backlog DUR]` starts a never-managed account: it learns no routes from INBOX history,
  and optionally lets the first `once` consider the last DUR of INBOX mail.

## Non-goals

- Several accounts in one process (decided: one process per account). No config inheritance.
- A `password_env` key: the password still never lives in a file.
- Service files / Docker (M6). Learning from user moves (M4).

## Anti-goals

- Orphaning an existing database or breaking an existing `--config` setup.
- A backlog sweep that moves flagged mail, or that happens before the user has seen a dry run.
- Two accounts sharing a database silently.

## Design

### `--account`
- Root persistent flag. Name must match `[a-z0-9_-]+` (no path separators). `--account` and an explicit
  `--config` together are an error.
- Config path: `<config dir>/NAME.toml` where config dir is `$XDG_CONFIG_HOME/quietbox` or `~/.config/quietbox`
  (the folder that already holds the default `config.toml`).
- Default state path with an account: `<state dir>/NAME.db` (state dir as today). With `--config` the default
  stays `quietbox.db`, so existing databases are not orphaned.
- Password: `QUIETBOX_IMAP_PASSWORD_<NAME>` (upper-cased, `-` to `_`), else `QUIETBOX_IMAP_PASSWORD`.
  The missing-password error names both.
- `quietbox accounts` lists `*.toml` in the config folder: name, host, username, database path (offline).

### Database account binding
Schema v4: table `meta(key, value)`; the account identity `username@host` (lower-case) is stored under
`account`. Every command that opens a database for an account binds it: an unbound database adopts the
account; a different account is refused with a message saying which account owns it and to set
`[state] path`. `init --dry-run` opens no database and is unaffected.

### `init --fresh [--backlog DUR]`
- `--fresh`: INBOX history is not learned as sender routes (Sent and typed folders still count). INBOX is
  only read when `--backlog` needs it. Not allowed with `--folder`.
- `--backlog DUR` (requires `--fresh`): the INBOX cursor starts at the first message whose arrival date is within
  DUR (else at the end, as today) instead of at the end. `cursors` gains `backlog_below`: the UIDNEXT read at
  the start of `init`. Messages below it are the backlog.
- `Process` ignores `skip_seen` for backlog messages (most backlog mail is already read) and still skips flagged
  ones, mail already moved once, and own/unparseable mail. This is my call, not the user's.
- `init` still moves nothing. Its summary (and `--dry-run`) says how many INBOX messages the backlog covers.
  The user reviews with `once --dry-run`, then `once`; moves are audited and undoable as usual.

## Constraints
CLAUDE.md safety rules. No new dependencies. Existing configs, databases and flags keep working.

## Approaches
Multi-account was weighed three ways (separate processes; one file with `[[account]]`; a directory of
configs run by one process). The user chose separate processes with `--account` for ergonomics.

## Left to the builder
Exact wording of messages, the `accounts` table layout, and test fixtures.

## Verification
Unit tests for path/password/state resolution, binding, Learn with fresh/backlog and Process with a
backlog; integration tests on Dovecot with throwaway users (fresh+backlog flow, account binding).
Not verifiable here: Gmail/Fastmail behaviour.
