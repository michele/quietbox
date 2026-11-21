# Accounts and fresh-account init: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `--account NAME`, per-account database binding, and `init --fresh [--backlog DUR]`.

**Architecture:** Config resolution (path, password env, state default) lives in `internal/config`; the CLI resolves flags once in `options.loadConfig` and opens databases through one helper that binds the account. `Learn` and `Process` gain the fresh/backlog behaviour; the engine's account model is unchanged.

**Tech Stack:** Go, existing dependencies only.

**Spec:** `docs/superpowers/specs/2026-11-21-accounts-and-fresh-design.md`. Read `CLAUDE.md` and the M3 plan first.

## Global Constraints

- Safety rules in CLAUDE.md. No new dependencies. Existing `--config` setups, `quietbox.db` default and the `QUIETBOX_IMAP_PASSWORD` variable keep working.
- Stage files by explicit path (an unrelated untracked file `env` sits in the repo root: never commit it).
- Each task: tests first (watch them fail), `make test`, `make lint`, one commit ending with `Claude-Session: https://claude.ai/code/session_01CUS7msYe81mSFUyxHhH6Nj`.

## Review Focus

1. Account name that tries path traversal (`../x`, `a/b`, empty, upper case) is refused (Task 2).
2. `--account` with an explicit `--config` is an error, not a silent pick (Task 3).
3. A database bound to account A is refused for account B, and an old unbound database is adopted without error (Task 1, 3).
4. `--backlog` without `--fresh`, or with `--folder`, is refused; `--fresh` never moves anything (Task 5).
5. Backlog messages that are flagged, already moved, or own-address mail are still left alone while read ones are filed (Task 4).

---

### Task 1: Store: account binding and backlog boundary

**Files:** Modify `internal/store/migrations.go`, `internal/store/store.go`; Test `internal/store/store_test.go`.

**Interfaces (produces):**
- Migration 4: `CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`; `ALTER TABLE cursors ADD COLUMN backlog_below INTEGER NOT NULL DEFAULT 0`.
- `store.Cursor` gains `BacklogBelow uint32`; `Cursor`, `SetCursor`, `(*Tx).SetCursor` read and write it.
- `var ErrWrongAccount = errors.New(...)`; `func (s *Store) BindAccount(ctx context.Context, account string) error`: stores `account` under meta key `account` when unset; nil when equal; an error wrapping `ErrWrongAccount` and naming both accounts otherwise.

- [ ] Tests: `TestMigration4KeepsOldData` (v3 database with a cursor row and audit row upgrades; `BacklogBelow == 0`), `TestBindAccountAdoptsThenRefusesOther` (first bind ok, same ok, other fails with `errors.Is(err, ErrWrongAccount)` and the message names the bound account), `TestCursorBacklogRoundTrip` (store and Tx).
- [ ] Implement; update the existing "simulate older schema" tests if they must also undo migration 4 (drop `meta`, drop the column).
- [ ] `make test lint`; commit `feat(store): account binding and backlog boundary`.

### Task 2: Config: account resolution

**Files:** Modify `internal/config/config.go`; Create `internal/config/account.go`; Test `internal/config/account_test.go`.

**Interfaces (produces):**
- `func ConfigDir(getenv func(string) string) string`: `$XDG_CONFIG_HOME/quietbox`, else `$HOME/.config/quietbox`.
- `func ValidAccountName(name string) error`: `[a-z0-9_-]+`, 1 to 64 chars.
- `func AccountConfigPath(getenv func(string) string, name string) (string, error)`.
- `func PasswordEnvFor(account string) string`: `QUIETBOX_IMAP_PASSWORD_<UPPER, - to _>`; `""` account gives `QUIETBOX_IMAP_PASSWORD`.
- `Load`, `LoadOffline`, `Parse` keep their signatures; add `LoadAccount(path, account string)`, `LoadAccountOffline(path, account string)` and `ParseAccount(data []byte, getenv func(string) string, account string)`; account changes only the password lookup (specific var first, then generic) and the default state path (`<state dir>/<account>.db` when `[state] path` is unset). The missing-password error names both variables when an account is set.
- `func AccountID(c *Config) string`: `lower(username)@lower(host)`.
- `func ListAccounts(getenv func(string) string) ([]AccountInfo, error)` with `AccountInfo{Name, Path, Host, Username, StatePath string; Err error}`: every `*.toml` in the config dir, loaded offline with its stem as account; a broken file yields `Err` instead of failing the list; missing dir yields an empty list.

- [ ] Tests (table-driven, assert exact values): name validation (`gmail`, `a-b_1` ok; ``, `A`, `../x`, `a/b`, `a.b`, 65 chars refused); `AccountConfigPath` with XDG and HOME; `PasswordEnvFor`; `ParseAccount` password precedence, missing-password message, state default `/state/quietbox/gmail.db`, explicit `[state] path` wins, `Parse` unchanged (`quietbox.db`); `AccountID`; `ListAccounts` with two good files, one broken and a missing dir.
- [ ] Implement; `make test lint`; commit `feat(config): per-account config, password and state resolution`.

### Task 3: CLI: `--account`, bound databases, `accounts`

**Files:** Modify `cmd/quietbox/main.go`, `check.go`, `init.go`, `scan.go`, `eval.go`, `senders.go`, `once.go`, `run.go`, `undo.go`; Create `cmd/quietbox/account.go`, `cmd/quietbox/accounts.go`; Test `cmd/quietbox/account_test.go`.

**Interfaces:**
- `options` gains `account string` and `configSet bool` (set in a root `PersistentPreRunE` from `cmd.Flags().Changed("config")`). New root persistent flag `--account`.
- `func (o *options) loadConfig(offline bool) (*config.Config, error)`: both flags set is an error naming both; `--account` validates the name, resolves the path with `config.AccountConfigPath(os.Getenv, ...)` and loads with `LoadAccount[Offline]`; otherwise today's behaviour. A missing account file error names the path it looked at.
- `func openState(ctx context.Context, cfg *config.Config, create bool) (*store.Store, error)`: `Open` or `OpenExisting` on `cfg.State.Path`, then `BindAccount(ctx, config.AccountID(cfg))`; closes the store on a bind error. Every command that opens a database uses it (`init`, `scan`, `senders`, `once`, `run`, `undo`).
- `newAccountsCmd(opts)`: prints `NAME  HOST  USERNAME  DATABASE`, errors inline per broken file, a hint when none exist.

- [ ] Tests: `--account` and `--config` together fail naming both; `--account gmail` reads `$XDG_CONFIG_HOME/quietbox/gmail.toml` (write a temp one, check the loaded host); unknown account error names the path; `--account ../x` refused; the password comes from `QUIETBOX_IMAP_PASSWORD_GMAIL` (set only that variable and `check` gets as far as dialing a closed port); `openState` adopts a fresh database and refuses one bound to another account with a message naming `[state] path`; `accounts` output lists a good and a broken file; `scan`/`once`/`run`/`undo` without a database still fail before connecting.
- [ ] Implement and switch the commands over; existing cmd tests must stay green.
- [ ] `make test lint`; commit `feat(cli): --account, account-bound databases, accounts command`.

### Task 4: Engine: fresh learning and backlog processing

**Files:** Modify `internal/engine/learn.go`, `internal/engine/learn_report.go`, `internal/engine/process.go`; Test `learn_test.go`, `process_test.go`.

**Interfaces:**
- `LearnOptions` gains `Fresh bool` and `Backlog time.Duration`; `Learn` returns an error for `Backlog > 0 && !Fresh`, and for `Fresh` with `Folder != ""`.
- With `Fresh`: INBOX messages add no observations (so no INBOX routes); INBOX is read only when `Backlog > 0`. Sent and the typed folders are unchanged.
- With `Backlog > 0`: read INBOX since `Now - Backlog` (no database writes); the INBOX cursor is `{UIDValidity, NextUID: lowest UID in that window (or UIDNEXT if none), BacklogBelow: UIDNEXT read at the start of INBOX's scan (0 if the window is empty)}`. `LearnReport` gains `Fresh bool`, `BacklogMessages int`, `BacklogSince time.Time`; `Write` prints a line `Backlog: N INBOX message(s) since <date> will be considered by the first 'quietbox once' (read mail included, flagged mail excluded)`. Dry run computes the count and writes nothing.
- `processInbox`: a message with `h.UID < cur.BacklogBelow` is not skipped for `\Seen` (still skipped for `\Flagged` when `skip_flagged`, for an audited key, and by classification for own/unparseable mail). After the cursor passes `BacklogBelow` it is stored as 0.

- [ ] Tests (Fake backend): `TestLearnFreshLearnsNoInboxRoutes` (alice in INBOX gets no route; a typed-folder sender and Sent-to still do); `TestLearnFreshWithoutBacklogDoesNotReadInbox` (OnFetch records no INBOX fetch) and the cursor is at UIDNEXT with `BacklogBelow == 0`; `TestLearnBacklogCursorAndBoundary` (messages older and newer than the window; cursor at the first in-window UID, boundary at UIDNEXT); `TestLearnBacklogEmptyWindow`; `TestLearnBacklogDryRunReportsCountWritesNothing`; `TestLearnBacklogRequiresFresh`; `TestLearnFreshRefusesFolderScope`; `TestProcessBacklogIgnoresSeen` (seen backlog mail from a sender the detectors file is moved; the same seen mail arriving after the boundary is not); `TestProcessBacklogStillSkipsFlaggedAndMoved`; `TestProcessBacklogBoundaryClearedAfterPass`.
- [ ] Implement; `make test lint`; commit `feat(engine): fresh-account learning and backlog sweep`.

### Task 5: `init --fresh --backlog`, integration tests, docs

**Files:** Modify `cmd/quietbox/init.go`; Create `cmd/quietbox/account_itest_test.go`; Modify `README.md`, `docs/PLAN.md`, `examples/fastmail.toml`.

- [ ] Flags `--fresh` (bool) and `--backlog DUR` (parsed with `config.ParseDuration`; `--backlog` without `--fresh` is an error that says so); passed into `LearnOptions`. Unit tests in `init_test.go`: the flag errors and parsing.
- [ ] Integration tests (`//go:build integration`, throwaway Dovecot user as in `m3_itest_test.go`; reuse its helpers): `TestItestFreshBacklogFlow` (INBOX holds an old and a recent message from a newsletter-looking sender and a read message from a person who is in Sent; `init --fresh --backlog 30d --dry-run` reports the count and creates no database; `init --fresh --backlog 30d`; `once --dry-run` lists the recent newsletter, not the old one and not the person; `once` files it; `undo` restores it), `TestItestFreshWithoutBacklogTouchesNothing` (existing INBOX mail stays after `once`), `TestItestAccountFlag` (write `NAME.toml` into a temp `XDG_CONFIG_HOME`, run `--account NAME init/once`; the database lands at `<XDG_STATE_HOME>/quietbox/NAME.db`), `TestItestWrongAccountDatabaseRefused` (two throwaway users, one `[state] path`).
- [ ] README: a "Several accounts" section (config per account, `--account`, per-account env vars, database and lock per account, `quietbox accounts`, one service per account) and a "Starting on a never-managed account" section (`init --fresh --backlog 30d`, then `once --dry-run`, then `once`; notes: read mail in the backlog is filed, flagged is not, `undo` reverses, no learning from your moves until M4). PLAN: record the design, the schema v4 change and that multi-account stays one process per account. `examples/fastmail.toml`: comment on `--account`.
- [ ] `make test lint`, `make itest`; commit `feat(cli): init --fresh --backlog, account integration tests, docs`.

---

## Self-review notes

- Spec coverage: `--account` and flag rules (T2, T3), paths/password/state defaults (T2), `accounts` (T2, T3), binding (T1, T3), fresh/backlog (T4, T5), backlog ignoring `skip_seen` (T4), docs (T5). Not covered by design: `password_env`, multi-account process.
- Calls made by the plan: default state path derives from the account only with `--account`; `BindAccount` adopts silently; `accounts` lists every `*.toml` including `config.toml`.
